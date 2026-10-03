// Package buildinfo holds the version and commit stamped into the binary at
// link time:
//
//	go build -ldflags "-X dash.cha.sh/internal/buildinfo.Version=v0.1.0 \
//	                   -X dash.cha.sh/internal/buildinfo.Commit=abc1234" ./cmd/chadash
package buildinfo

import (
	"fmt"
	"runtime"
)

// Both default to "dev" so an unstamped build is obvious in logs and bug reports.
var (
	Version = "dev"
	Commit  = "dev"
)

// String renders a one-line description for `chadash version` and startup logs.
func String() string {
	return fmt.Sprintf("chadash %s (commit %s, %s %s/%s)", Version, Commit, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
