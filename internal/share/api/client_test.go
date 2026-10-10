package api_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/secretli/cli/internal/share/api"
)

// answering is a server that gives every request this status and JSON body.
func answering(t *testing.T, status int, body string) *api.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return api.New(srv.URL)
}

// liveSecret is a live secret's metadata as the server sends it.
func liveSecret(burnAfterRead, opened bool) string {
	return fmt.Sprintf(`{"encrypted_meta":"v2$abc","blob_size":359,"burn_after_read":%t,"expires_at":"2026-10-08T20:14:56Z","created_at":"2026-10-08T20:09:56Z","opened":%t}`, burnAfterRead, opened)
}

// The server tells that a recipient opened a reusable secret, not when.
func TestMetadataReadsOpened(t *testing.T) {
	cases := []struct {
		name          string
		burnAfterRead bool
		opened        bool
	}{
		{"reusable and opened", false, true},
		{"reusable and not opened", false, false},
		{"one-time", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta, err := answering(t, http.StatusOK, liveSecret(tc.burnAfterRead, tc.opened)).Metadata(context.Background(), "id", "token")
			if err != nil {
				t.Fatal(err)
			}
			if meta.Opened != tc.opened || meta.BurnAfterRead != tc.burnAfterRead {
				t.Errorf("Opened = %t, BurnAfterRead = %t, want %t, %t", meta.Opened, meta.BurnAfterRead, tc.opened, tc.burnAfterRead)
			}
			// Nothing else about the answer depends on it.
			if meta.EncryptedMeta != "v2$abc" || meta.BlobSize != 359 || !meta.ExpiresAt.Equal(time.Date(2026, 10, 8, 20, 14, 56, 0, time.UTC)) || !meta.CreatedAt.Equal(time.Date(2026, 10, 8, 20, 9, 56, 0, time.UTC)) {
				t.Errorf("metadata = %+v", meta)
			}
		})
	}
}

// shortTimeout makes the clients made from now on wait this long for an
// answer to begin.
func shortTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	before := api.ResponseHeaderTimeout
	api.ResponseHeaderTimeout = d
	t.Cleanup(func() { api.ResponseHeaderTimeout = before })
}

// A server that takes the request and never answers ends the request after
// the timeout, as an error that says so, and is not asked again.
func TestAServerThatNeverAnswers(t *testing.T) {
	shortTimeout(t, 100*time.Millisecond)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		// The client gives up and closes the connection, which the server
		// notices once it has read the request, and which ends this.
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	// Without the timeout, this one ends the test instead of the hang.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := time.Now()
	_, err := api.New(srv.URL).Metadata(ctx, "id", "token")
	var apiErr *api.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 0 || !apiErr.Timeout {
		t.Fatalf("err = %#v", err)
	}
	if want := "network error: " + srv.URL + " did not answer in time"; err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
	if n := requests.Load(); n != 1 {
		t.Errorf("asked %d times", n)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("gave up after %s", elapsed)
	}
}

// A timeout while connecting comes before the request is sent, so the
// request is tried again, like after any failed connection.
func TestADialTimeoutIsTriedAgain(t *testing.T) {
	c := api.New("http://192.0.2.1")
	var dials atomic.Int32
	c.HTTP.Transport.(*http.Transport).DialContext = func(_ context.Context, network, addr string) (net.Conn, error) {
		dials.Add(1)
		// What a dialer says when its timeout runs out.
		return nil, &net.OpError{Op: "dial", Net: network, Addr: &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 80}, Err: os.ErrDeadlineExceeded}
	}

	_, err := c.Metadata(context.Background(), "id", "token")
	var apiErr *api.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 0 || apiErr.Timeout || !strings.Contains(err.Error(), "i/o timeout") {
		t.Fatalf("err = %v", err)
	}
	if n := dials.Load(); n != api.MaxTransientAttempts {
		t.Errorf("connected %d times, want %d", n, api.MaxTransientAttempts)
	}
}

// So does a timeout in the TLS handshake: a server that takes the
// connection and never answers the handshake.
func TestATLSHandshakeTimeoutIsTriedAgain(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu    sync.Mutex
		conns []net.Conn
	)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range conns {
			_ = conn.Close()
		}
	})

	c := api.New("https://" + ln.Addr().String())
	c.HTTP.Transport.(*http.Transport).TLSHandshakeTimeout = 50 * time.Millisecond
	_, err = c.Metadata(context.Background(), "id", "token")
	var apiErr *api.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 0 || apiErr.Timeout || !strings.Contains(err.Error(), "TLS handshake timeout") {
		t.Fatalf("err = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(conns) != api.MaxTransientAttempts {
		t.Errorf("connected %d times, want %d", len(conns), api.MaxTransientAttempts)
	}
}

// The timeout is for the answer to begin: a body that takes longer to
// arrive, as a large download does, is read to the end.
func TestTheTimeoutLeavesTheBodyAlone(t *testing.T) {
	shortTimeout(t, 100*time.Millisecond)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "0123")
		w.(http.Flusher).Flush()
		time.Sleep(300 * time.Millisecond)
		_, _ = io.WriteString(w, "4567")
	}))
	t.Cleanup(srv.Close)

	data, err := api.New(srv.URL).ReadRange(context.Background(), "id", "session", 0, 7)
	if err != nil || string(data) != "01234567" {
		t.Errorf("read %q, %v", data, err)
	}
}

// Failures that come quickly are still tried again.
func TestServerErrorsAreTriedAgain(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) < api.MaxTransientAttempts {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(w, liveSecret(false, false))
	}))
	t.Cleanup(srv.Close)

	if _, err := api.New(srv.URL).Metadata(context.Background(), "id", "token"); err != nil || requests.Load() != api.MaxTransientAttempts {
		t.Errorf("asked %d times, %v", requests.Load(), err)
	}
}
