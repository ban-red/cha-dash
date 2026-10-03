package buildinfo

import (
	"runtime"
	"strings"
	"testing"
)

func TestString(t *testing.T) {
	got := String()
	if !strings.HasPrefix(got, "chadash dev (commit dev, ") || !strings.Contains(got, runtime.Version()) {
		t.Fatalf("String() = %q", got)
	}

	Version, Commit = "v0.1.0", "abc1234"
	t.Cleanup(func() { Version, Commit = "dev", "dev" })
	if got := String(); !strings.HasPrefix(got, "chadash v0.1.0 (commit abc1234, ") {
		t.Fatalf("stamped String() = %q", got)
	}
}
