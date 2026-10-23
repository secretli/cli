package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/secretli/cli/internal/share"
	"github.com/secretli/format/go/bundle"
	"github.com/secretli/format/go/transfer"
)

// maxStatusWait bounds how long transfer_status waits for a hand-over to
// end, well below the time clients give a tool call.
const maxStatusWait = 30 * time.Second

func newMCPCmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Let an AI assistant send with a code, over MCP",
		Long: `Serves the Model Context Protocol on stdin and stdout, so that an assistant
such as Claude Desktop can hand files or text to another device with a short
code, the way share --code does. Encryption happens here, in this process;
the assistant only ever sees the code, never a link.

Two tools: send encrypts, uploads and returns the code while the transfer
waits in the background; transfer_status says whether it was handed over.
Transfers still waiting end when the assistant closes the connection.

The assistant starts this command itself. In claude_desktop_config.json:

  {"mcpServers": {"secretli": {"command": "secretli", "args": ["mcp"]}}}`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: run(func(cmd *cobra.Command, _ []string) error {
			return e.serveMCP(cmd.Context())
		}),
	}
}

// serveMCP answers MCP requests on stdin and stdout until the client goes
// away, then releases the transfers still waiting.
func (e *env) serveMCP(ctx context.Context) error {
	if _, err := share.Origin(e.server); err != nil {
		return err
	}
	server, transfers := e.newMCPServer(ctx)
	err := server.Run(ctx, &mcp.IOTransport{Reader: e.stdin, Writer: nopWriteCloser{e.stdout}})
	transfers.stop()
	if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// newMCPServer is the MCP server with its tools. Transfers live on ctx,
// not on the request that started them.
func (e *env) newMCPServer(ctx context.Context) (*mcp.Server, *handovers) {
	transfers := newHandovers(ctx)
	server := mcp.NewServer(&mcp.Implementation{Name: "secretli", Title: "Secretli", Version: version()}, &mcp.ServerOptions{
		Instructions: `Hands files or text to the user's other device through Secretli. Call send, ` +
			`tell the user the code and how to receive it, then call transfer_status until ` +
			`it is no longer "waiting". A code works once and for about ten minutes.`,
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:  "send",
		Title: "Send with a code",
		Description: `Encrypts files or text on this machine, uploads only the ciphertext to ` +
			e.server + ` and returns a short code like 7-acid-rocket for the user's other device. ` +
			`The secret opens once. Give either paths or text. The transfer waits in the ` +
			`background; transfer_status tells when it was handed over.`,
		Annotations: &mcp.ToolAnnotations{OpenWorldHint: new(true), DestructiveHint: new(false)},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpSendInput) (*mcp.CallToolResult, mcpSendOutput, error) {
		out, err := e.mcpSend(ctx, req, transfers, in)
		return nil, out, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:  "transfer_status",
		Title: "Hand-over status",
		Description: `Says whether a code from send was handed over: "waiting", "delivered", ` +
			`"failed" (with the reason; send again for a new code) or "unknown" (not sent ` +
			`by this server, which may have restarted). wait_seconds, up to 30, waits for ` +
			`the hand-over to end before answering.`,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpStatusInput) (*mcp.CallToolResult, mcpStatusOutput, error) {
		out, err := transfers.status(ctx, in)
		return nil, out, err
	})
	return server, transfers
}

type mcpSendInput struct {
	Paths   []string `json:"paths,omitempty" jsonschema:"absolute paths of the files to send"`
	Text    string   `json:"text,omitempty" jsonschema:"text to send, instead of files"`
	Name    string   `json:"name,omitempty" jsonschema:"send the text as a file with this name, instead of as text"`
	Expires string   `json:"expires,omitempty" jsonschema:"how long the secret lives if it isn't opened: 5m, 10m, 15m, 1h, 4h, 12h, 1d, 3d or 7d; 1d if left out"`
}

type mcpSendOutput struct {
	Code          string     `json:"code" jsonschema:"the code to type on the other device"`
	CodeExpiresAt time.Time  `json:"code_expires_at" jsonschema:"when the code stops working"`
	Receive       string     `json:"receive" jsonschema:"how to use the code on the other device"`
	ExpiresAt     time.Time  `json:"expires_at" jsonschema:"when the secret expires if it isn't opened"`
	Kind          share.Kind `json:"kind" jsonschema:"text or files"`
	Files         []string   `json:"files,omitempty" jsonschema:"the names the files arrive under"`
	Size          int64      `json:"size" jsonschema:"bytes before encryption"`
}

type mcpStatusInput struct {
	Code        string `json:"code" jsonschema:"the code send returned"`
	WaitSeconds int    `json:"wait_seconds,omitempty" jsonschema:"wait up to this many seconds, at most 30, for the hand-over to end"`
}

type mcpStatusOutput struct {
	State  string `json:"state" jsonschema:"waiting, delivered, failed or unknown"`
	Reason string `json:"reason,omitempty" jsonschema:"why it failed"`
}

// mcpSend makes a secret and opens a transfer for it. It returns once the
// code is there; the transfer goes on in transfers.
func (e *env) mcpSend(ctx context.Context, req *mcp.CallToolRequest, transfers *handovers, in mcpSendInput) (mcpSendOutput, error) {
	expires := in.Expires
	if expires == "" {
		expires = share.DefaultExpiration
	}
	if !share.ValidExpiration(expires) {
		return mcpSendOutput{}, fmt.Errorf("expires must be one of %s", strings.Join(share.Expirations, ", "))
	}
	server, err := share.Origin(e.server)
	if err != nil {
		return mcpSendOutput{}, err
	}
	params := share.Params{Expiration: expires}
	switch {
	case len(in.Paths) > 0 && in.Text != "":
		return mcpSendOutput{}, errors.New("send paths or text, not both at once")
	case len(in.Paths) > 0:
		for _, path := range in.Paths {
			if !filepath.IsAbs(path) {
				return mcpSendOutput{}, fmt.Errorf("%s: give an absolute path", path)
			}
			src, f, err := fileSource(path)
			if err != nil {
				return mcpSendOutput{}, err
			}
			defer func() { _ = f.Close() }()
			params.Files = append(params.Files, src)
		}
	case in.Text != "" && in.Name != "":
		params.Files = []bundle.Source{memorySource(in.Name, []byte(in.Text))}
	case in.Text != "":
		params.Text = []byte(in.Text)
	default:
		return mcpSendOutput{}, errors.New("nothing to send: give paths or text")
	}
	if token := req.Params.GetProgressToken(); token != nil {
		params.Progress = func(uploaded, total int64) {
			_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
				ProgressToken: token, Message: "Encrypting and uploading", Progress: float64(uploaded), Total: float64(total),
			})
		}
	}

	result, err := share.Share(ctx, e.client(server), params)
	if err != nil {
		return mcpSendOutput{}, err
	}
	link := result.Link.Recipient()
	origin, err := share.Origin(link.Origin)
	if err != nil {
		return mcpSendOutput{}, err
	}
	opened, err := transfers.start(ctx, func(ctx context.Context, ready func(transfer.Code, time.Time)) error {
		return share.SendWithCode(ctx, e.client(origin), link.String(), ready)
	})
	if err != nil {
		return mcpSendOutput{}, err
	}
	page, command := receiveWays(opened.code, origin)
	return mcpSendOutput{
		Code:          opened.code.String(),
		CodeExpiresAt: opened.expiresAt.UTC(),
		Receive:       fmt.Sprintf("On the other device, open %s and type the code, or run %s there.", page, command),
		ExpiresAt:     result.ExpiresAt.UTC(),
		Kind:          result.Kind,
		Files:         result.Names,
		Size:          result.Size,
	}, nil
}

// handovers are the transfers this server opened, by code. They outlive the
// requests that started them, until the server stops.
type handovers struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	byCode map[string]*handover
}

type handover struct {
	code      transfer.Code
	expiresAt time.Time
	done      chan struct{}
	err       error // set before done is closed
}

func (h *handover) ended() bool {
	select {
	case <-h.done:
		return true
	default:
		return false
	}
}

func newHandovers(ctx context.Context) *handovers {
	ctx, cancel := context.WithCancel(ctx)
	return &handovers{ctx: ctx, cancel: cancel, byCode: map[string]*handover{}}
}

// start runs send in the background and returns once it has a code. A
// request that ends before then takes the transfer with it.
func (hs *handovers) start(ctx context.Context, send func(context.Context, func(transfer.Code, time.Time)) error) (*handover, error) {
	h := &handover{done: make(chan struct{})}
	ready := make(chan struct{})
	tctx, cancel := context.WithCancel(hs.ctx)
	hs.wg.Go(func() {
		defer cancel()
		h.err = send(tctx, func(code transfer.Code, expiresAt time.Time) {
			h.code, h.expiresAt = code, expiresAt
			close(ready)
		})
		close(h.done)
	})
	select {
	case <-ready:
		hs.mu.Lock()
		hs.byCode[h.code.String()] = h
		hs.mu.Unlock()
		return h, nil
	case <-h.done:
		return nil, describeSendError(h.err)
	case <-ctx.Done():
		cancel()
		<-h.done
		return nil, ctx.Err()
	}
}

func (hs *handovers) status(ctx context.Context, in mcpStatusInput) (mcpStatusOutput, error) {
	code, err := transfer.ParseCode(in.Code)
	if err != nil {
		return mcpStatusOutput{}, errors.New("codes look like 7-acid-rocket: a number and two words")
	}
	hs.mu.Lock()
	h := hs.byCode[code.String()]
	hs.mu.Unlock()
	if h == nil {
		return mcpStatusOutput{State: "unknown"}, nil
	}
	if !h.ended() {
		wait := min(max(time.Duration(in.WaitSeconds)*time.Second, 0), maxStatusWait)
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-h.done:
		case <-timer.C:
			return mcpStatusOutput{State: "waiting"}, nil
		case <-ctx.Done():
			return mcpStatusOutput{}, ctx.Err()
		}
	}
	if h.err != nil {
		return mcpStatusOutput{State: "failed", Reason: describeSendError(h.err).Error()}, nil
	}
	return mcpStatusOutput{State: "delivered"}, nil
}

// stop ends the transfers still waiting and waits until the other sides
// were told, which SendWithCode bounds.
func (hs *handovers) stop() {
	hs.cancel()
	hs.wg.Wait()
}
