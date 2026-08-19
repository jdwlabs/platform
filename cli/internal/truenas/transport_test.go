package truenas

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// The sentinel prints "do not re-run, each attempt erodes the credential". That
// is only true when the key was actually evaluated. A middleware that renamed
// or removed the login method answers before reaching one, and telling an
// operator to stop re-running for that teaches them to ignore the real warning.
func TestIsCredentialRejection_OnlyForErrorsThatReachedTheKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"method renamed away", &rpcError{Code: rpcMethodNotFound, Message: "Method not found"}, false},
		{"malformed request", &rpcError{Code: rpcInvalidRequest, Message: "Invalid Request"}, false},
		{"parameters rejected", &rpcError{Code: rpcInvalidParams, Message: "Invalid params"}, false},
		{"unparseable frame", &rpcError{Code: rpcParseError, Message: "Parse error"}, false},
		{"middleware refused the key", &rpcError{Code: 22, Message: "Invalid API key"}, true},
		{"wrapped rejection", fmt.Errorf("auth: %w", &rpcError{Code: 22}), true},
		{"connection dropped", context.DeadlineExceeded, false},
		{"decode failure", errors.New("decode auth.login_with_api_key result: unexpected EOF"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCredentialRejection(tc.err); got != tc.want {
				t.Errorf("isCredentialRejection(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
