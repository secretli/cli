package sharetest

import (
	"os"
	"strings"
	"testing"
)

// Target is the server a test talks to: the fake by default, or a real
// Secretli server when SECRETLI_TEST_SERVER names one. Running the same
// tests against both checks that the fake behaves like the real thing.
type Target struct {
	URL  string
	fake *Server
}

// Start returns the fake, started with this part size, or the real server
// named by SECRETLI_TEST_SERVER, which states its own part size. A real
// server needs raised rate limits (RATE_LIMIT_MULTIPLIER), since the tests
// send many requests from one address.
func Start(t testing.TB, partSize int64) *Target {
	t.Helper()
	if url := strings.TrimRight(os.Getenv("SECRETLI_TEST_SERVER"), "/"); url != "" {
		return &Target{URL: url}
	}
	srv := New(partSize)
	t.Cleanup(srv.Close)
	return &Target{URL: srv.URL, fake: srv}
}

// Fake returns the fake for checks that look inside the server, or nil
// when the test runs against a real server, which cannot be looked into.
func (tg *Target) Fake() *Server { return tg.fake }
