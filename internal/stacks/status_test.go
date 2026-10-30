package stacks

import (
	"errors"
	"net/http"
	"testing"

	"dash.cha.sh/internal/stackproto"
)

// TestForbiddenIs403: a node policy refusal (a remote outside
// stacks.remotes) reaches the browser as 403, even flattened to text.
func TestForbiddenIs403(t *testing.T) {
	for _, err := range []error{
		stackproto.Errorf(stackproto.CodeForbidden, "git.other.example isn't in this node's stacks.remotes"),
		errors.New("node local: forbidden: git.other.example isn't in this node's stacks.remotes"),
	} {
		status, code, msg := statusOf(err)
		if status != http.StatusForbidden || code != "forbidden" || msg != "git.other.example isn't in this node's stacks.remotes" {
			t.Errorf("statusOf(%v) = %d %s %q", err, status, code, msg)
		}
	}
}
