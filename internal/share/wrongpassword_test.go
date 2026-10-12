package share

import (
	"errors"
	"testing"

	"github.com/secretli/cli/internal/share/api"
)

// The count comes from the server's error details; anything that isn't a
// small whole number is no count at all.
func TestWrongPasswordReadsTheCount(t *testing.T) {
	for _, tc := range []struct {
		details map[string]any
		left    int
		message string
	}{
		{map[string]any{"attempts_left": 9.0}, 9, "wrong password; 9 tries left before the secret is deleted"},
		{map[string]any{"attempts_left": 1.0}, 1, "wrong password; 1 try left before the secret is deleted"},
		{map[string]any{"attempts_left": 0.0}, 0, "wrong password, and the last one this secret takes: the server deleted it"},
		{nil, -1, "wrong password"},
		{map[string]any{"attempts_left": "9"}, -1, "wrong password"},
		{map[string]any{"attempts_left": 2.5}, -1, "wrong password"},
		{map[string]any{"attempts_left": -1.0}, -1, "wrong password"},
		{map[string]any{"attempts_left": 1e9}, -1, "wrong password"},
	} {
		err := wrongPassword(&api.Error{Status: 403, Message: "invalid blob token", Details: tc.details})
		wrong, ok := errors.AsType[*WrongPasswordError](err)
		if !ok || wrong.AttemptsLeft != tc.left || err.Error() != tc.message || !errors.Is(err, ErrWrongPassword) {
			t.Errorf("details %v: err = %v (%+v), want %d left and %q", tc.details, err, wrong, tc.left, tc.message)
		}
	}
}
