package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpClient connects a client to the MCP server for the given server URL.
// The server's transfers end with the test.
func mcpClient(t *testing.T, serverURL string) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	e := &env{server: serverURL}
	server, transfers := e.newMCPServer(ctx)
	serverSide, clientSide := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, serverSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cs.Close()
		_ = ss.Wait()
		transfers.stop()
	})
	return cs
}

// callTool calls a tool and returns its raw result as JSON, and its
// structured output decoded into out unless the call failed.
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any) (raw string, failed bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		return string(b), true
	}
	structured, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(structured, out); err != nil {
		t.Fatalf("%s: %v in %s", name, err, structured)
	}
	return string(b), false
}

func TestMCPSendsFilesWithACode(t *testing.T) {
	srv := fakeServer(t)
	cs := mcpClient(t, srv.URL)

	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("from the assistant\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var sent mcpSendOutput
	raw, failed := callTool(t, cs, "send", map[string]any{"paths": []string{path}}, &sent)
	if failed {
		t.Fatalf("send failed: %s", raw)
	}
	if !regexp.MustCompile(`^[1-9][0-9]{0,2}-[a-z]+-[a-z]+$`).MatchString(sent.Code) {
		t.Fatalf("code = %q", sent.Code)
	}
	// The assistant sees the code and nothing that opens the secret.
	if strings.Contains(raw, "/s#") || strings.Contains(raw, "link") {
		t.Errorf("result carries a link: %s", raw)
	}
	if sent.Kind != "files" || !slices.Equal(sent.Files, []string{"notes.txt"}) || sent.Size != 19 ||
		!strings.Contains(sent.Receive, "/c and type the code") || !strings.Contains(sent.Receive, "secretli receive "+sent.Code+" --server "+srv.URL) {
		t.Errorf("sent = %+v", sent)
	}

	var status mcpStatusOutput
	if raw, _ := callTool(t, cs, "transfer_status", map[string]any{"code": sent.Code}, &status); status.State != "waiting" {
		t.Fatalf("before receiving: %s", raw)
	}

	out := t.TempDir()
	if _, stderr, code := runCLI(t, "", "receive", sent.Code, "--server="+srv.URL, "--yes", "--out", out); code != 0 {
		t.Fatalf("receive: exit %d, %q", code, stderr)
	}
	if got, err := os.ReadFile(filepath.Join(out, "notes.txt")); err != nil || string(got) != "from the assistant\n" {
		t.Fatalf("received %q, %v", got, err)
	}
	// Typed loosely, as people do.
	loose := strings.ToUpper(strings.ReplaceAll(sent.Code, "-", " "))
	if raw, _ := callTool(t, cs, "transfer_status", map[string]any{"code": loose, "wait_seconds": 10}, &status); status.State != "delivered" {
		t.Fatalf("after receiving: %s", raw)
	}
}

func TestMCPSendsText(t *testing.T) {
	srv := fakeServer(t)
	cs := mcpClient(t, srv.URL)

	var sent mcpSendOutput
	if raw, failed := callTool(t, cs, "send", map[string]any{"text": "echo hello\n", "expires": "1h"}, &sent); failed || sent.Kind != "text" {
		t.Fatalf("send: %s", raw)
	}
	stdout, stderr, code := runCLI(t, "", "receive", sent.Code, "--server="+srv.URL, "--yes")
	if code != 0 || stdout != "echo hello\n" {
		t.Fatalf("receive: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestMCPRefusesWhatItCannotSend(t *testing.T) {
	srv := fakeServer(t)
	cs := mcpClient(t, srv.URL)
	abs := filepath.Join(t.TempDir(), "missing.txt")
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{}, "nothing to send"},
		{map[string]any{"paths": []string{"notes.txt"}}, "give an absolute path"},
		{map[string]any{"paths": []string{abs}}, "no such file"},
		{map[string]any{"paths": []string{t.TempDir()}}, "is a directory"},
		{map[string]any{"paths": []string{abs}, "text": "both"}, "not both"},
		{map[string]any{"text": "hi", "expires": "2h"}, "expires must be one of"},
	} {
		if raw, failed := callTool(t, cs, "send", tc.args, nil); !failed || !strings.Contains(raw, tc.want) {
			t.Errorf("send %v: failed %v, %s", tc.args, failed, raw)
		}
	}
}

func TestMCPStatusOfCodesItDidNotSend(t *testing.T) {
	cs := mcpClient(t, fakeServer(t).URL)
	var status mcpStatusOutput
	if raw, _ := callTool(t, cs, "transfer_status", map[string]any{"code": "7-acid-rocket"}, &status); status.State != "unknown" {
		t.Errorf("unknown code: %s", raw)
	}
	if raw, failed := callTool(t, cs, "transfer_status", map[string]any{"code": "acid"}, nil); !failed || !strings.Contains(raw, "codes look like") {
		t.Errorf("not a code: %s", raw)
	}
}

// When the assistant goes away, transfers still waiting end, so the other
// device isn't left waiting for the code to expire.
func TestMCPReleasesWaitingTransfersWhenItStops(t *testing.T) {
	srv := fakeServer(t)
	ctx := context.Background()
	e := &env{server: srv.URL}
	server, transfers := e.newMCPServer(ctx)
	serverSide, clientSide := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, serverSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	var sent mcpSendOutput
	if raw, failed := callTool(t, cs, "send", map[string]any{"text": "never picked up"}, &sent); failed {
		t.Fatalf("send: %s", raw)
	}
	_ = cs.Close()
	_ = ss.Wait()
	transfers.stop()

	if _, stderr, code := runCLI(t, "", "receive", sent.Code, "--server="+srv.URL, "--yes"); code != ExitGone {
		t.Errorf("receive after stop: exit %d, %q", code, stderr)
	}
}

// The command speaks MCP on its own stdin and stdout, and nothing else
// lands on stdout.
func TestMCPCommandServesOnStdio(t *testing.T) {
	srv := fakeServer(t)
	clientIn, serverOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	serverIn, clientOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		done <- Main([]string{"mcp", "--server=" + srv.URL}, serverIn, serverOut, stderr)
		_ = serverOut.Close()
	}()

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(context.Background(), &mcp.IOTransport{Reader: clientIn, Writer: clientOut}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cs.InitializeResult().ServerInfo.Name != "secretli" {
		t.Errorf("server info = %+v", cs.InitializeResult().ServerInfo)
	}
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	if !slices.Equal(names, []string{"send", "transfer_status"}) {
		t.Errorf("tools = %v", names)
	}
	_ = cs.Close()
	if code := <-done; code != 0 {
		got, _ := os.ReadFile(stderr.Name())
		t.Errorf("exit %d, stderr %q", code, got)
	}
}
