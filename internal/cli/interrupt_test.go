package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Ctrl-C cancels the command's context. A command waiting for input must
// give up then, instead of waiting on until the input arrives.
func TestAnInterruptEndsAWaitingRead(t *testing.T) {
	for _, args := range [][]string{
		{"open"},                                 // the link from stdin
		{"receive", "--yes"},                     // the code from stdin
		{"share", "--server=http://127.0.0.1:1"}, // the text from stdin
	} {
		t.Run(args[0], func(t *testing.T) {
			// stdin is a pipe that stays open and never delivers anything.
			stdin, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			defer writer.Close()
			dir := t.TempDir()
			stdout, _ := os.Create(filepath.Join(dir, "stdout"))
			stderr, _ := os.Create(filepath.Join(dir, "stderr"))
			defer stdout.Close()
			defer stderr.Close()

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan int, 1)
			go func() { done <- execute(ctx, args, stdin, stdout, stderr) }()
			time.Sleep(50 * time.Millisecond) // let it reach the read
			cancel()

			select {
			case code := <-done:
				said, _ := os.ReadFile(filepath.Join(dir, "stderr"))
				if code != ExitError || !strings.Contains(string(said), "interrupted") {
					t.Errorf("exit %d, stderr %q", code, said)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("still waiting for input after the interrupt")
			}
		})
	}
}

func TestInterruptibleReturnsWhatWasRead(t *testing.T) {
	got, err := interruptible(context.Background(), func() (string, error) { return "line", nil })
	if err != nil || got != "line" {
		t.Errorf("got %q, %v", got, err)
	}
}
