package apidoc

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"dash.cha.sh/internal/authz"
)

// Removed are routes a release had that this one doesn't, each with the
// version that deprecated it. A route is deprecated (Op.Deprecated) in a
// released version before it's removed; CONTRIBUTING.md's rule is that it
// goes no sooner than the next minor version, which review checks, since a
// test can't know which version is being cut.
var Removed = map[string]string{}

// TestDeprecationRule: every route of the newest released version
// (testdata/released/<version>.txt, written by bun run release:cut) is
// still here, or in Removed having been deprecated in a version up to that
// one; and every deprecation names a version.
func TestDeprecationRule(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "released", "*.txt"))
	if len(files) == 0 {
		t.Fatal("no released route lists")
	}
	slices.SortFunc(files, func(a, b string) int { return compareVersions(versionOf(a), versionOf(b)) })
	newest := files[len(files)-1]
	released := versionOf(newest)
	now := map[string]bool{}
	for _, r := range authz.Routes {
		now[r.Pattern] = true
	}
	f, err := os.Open(newest)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		p := strings.TrimSpace(sc.Text())
		if p == "" {
			continue
		}
		n++
		if now[p] {
			continue
		}
		in, ok := Removed[p]
		switch {
		case !ok:
			t.Errorf("%s was in %s and is gone: deprecate it first (Op.Deprecated), for at least one minor version, then list it in Removed", p, released)
		case compareVersions(in, released) > 0:
			t.Errorf("%s is removed, but was deprecated in %s, after %s: it needs a release while deprecated", p, in, released)
		}
	}
	if n < 100 {
		t.Fatalf("%s lists only %d routes", newest, n)
	}
	for pattern, op := range rows {
		if op.Deprecated != "" && minorOf(op.Deprecated) == "" {
			t.Errorf("%s: Deprecated %q isn't a version", pattern, op.Deprecated)
		}
	}
}

func versionOf(path string) string { return strings.TrimSuffix(filepath.Base(path), ".txt") }

func minorOf(v string) string {
	parts := strings.SplitN(strings.TrimPrefix(v, "v"), ".", 3)
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "." + parts[1]
}

func compareVersions(a, b string) int {
	pa, pb := strings.Split(strings.TrimPrefix(a, "v"), "."), strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := range max(len(pa), len(pb)) {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(strings.SplitN(pa[i], "-", 2)[0])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(strings.SplitN(pb[i], "-", 2)[0])
		}
		if x != y {
			return x - y
		}
	}
	return 0
}

func TestCompareVersions(t *testing.T) {
	if compareVersions("0.4.0", "0.10.0") >= 0 || compareVersions("0.5.1", "0.5.0") <= 0 || compareVersions("1.0.0", "1.0.0") != 0 {
		t.Fatal("compareVersions")
	}
}
