package msg

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCatalog(t *testing.T) {
	for _, p := range Check() {
		t.Error(p)
	}
	if len(IDs()) < 40 {
		t.Fatalf("only %d sentences registered", len(IDs()))
	}
}

func TestText(t *testing.T) {
	register(map[string]string{"test.node_down": "{name} isn't running on {host}"})
	defer delete(catalog, "test.node_down")

	cases := []struct {
		m    M
		want string
	}{
		{New("test.node_down", "name", "web", "host", "nas"), "web isn't running on nas"},
		{New("auth.stepup_bad_body"), `send {"password"} or a passkey assertion`},
		{New("hub.internal_error"), "internal error"},
		// Mistakes render as the ID and the arguments, never a panic.
		{New("test.nope"), "test.nope"},
		{New("test.node_down", "name", "web"), "test.node_down (name=web)"},
		{New("test.node_down", "name", "web", "host", "nas", "port", 80), "test.node_down (host=nas, name=web, port=80)"},
		{New("test.node_down", "name"), "test.node_down (name=<nil>)"},
	}
	for _, c := range cases {
		if got := c.m.Text(); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.m, got, c.want)
		}
	}
	if got := CheckArgs("test.node_down", []string{"name", "port"}); len(got) != 2 {
		t.Errorf("CheckArgs: %q, want a missing {host} and an extra port", got)
	}
	if got := CheckArgs("test.nope", nil); len(got) != 1 {
		t.Errorf("CheckArgs on an unknown ID: %q", got)
	}
}

func TestJSON(t *testing.T) {
	b, _ := json.Marshal(New("auth.unknown_host", "host", "nas.example"))
	if string(b) != `{"id":"auth.unknown_host","args":{"host":"nas.example"}}` {
		t.Errorf("got %s", b)
	}
	b, _ = json.Marshal(New("hub.internal_error"))
	if string(b) != `{"id":"hub.internal_error"}` {
		t.Errorf("got %s", b)
	}
}

// TestCallSites checks every msg.New in the hub's source: a literal ID that
// is in the catalog, literal argument names, and exactly the sentence's
// parameters.
func TestCallSites(t *testing.T) {
	calls := 0
	walkGo(t, func(path string, f *ast.File) {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isSel(call.Fun, "msg", "New") {
				return true
			}
			calls++
			id, ok := stringLit(call.Args[0])
			if !ok {
				t.Errorf("%s: msg.New needs a literal ID, so the catalog can be checked", path)
				return true
			}
			var keys []string
			for i := 1; i < len(call.Args); i += 2 {
				k, ok := stringLit(call.Args[i])
				if !ok {
					t.Errorf("%s: %s: argument names must be literals", path, id)
					return true
				}
				keys = append(keys, k)
			}
			if (len(call.Args)-1)%2 != 0 {
				t.Errorf("%s: %s: an argument name without a value", path, id)
			}
			for _, p := range CheckArgs(id, keys) {
				t.Errorf("%s: %s", path, p)
			}
			return true
		})
	})
	if calls < 40 {
		t.Fatalf("only %d msg.New calls found; the walk isn't seeing the code", calls)
	}
}

// migrated lists the packages whose errors all come from the catalog. Each
// later wave adds its packages here.
var migrated = []string{
	"internal/httpx",
	"internal/authz",
	"internal/auth",
	"internal/updates",
	// Wave 28: new packages, every sentence from the start.
	"internal/incidents",
	"internal/timeline",
	"internal/pvehook",
}

// TestMigratedPackagesSay fails when a migrated package writes an error
// with a literal sentence: httpx.Error(w, status, code, "...") or an
// ErrorDetail{Message: "..."}. Those use httpx.Say and a catalog entry.
// Sentences that arrive as values (a validation error's text) may still go
// through httpx.Error until their sources move to the catalog.
func TestMigratedPackagesSay(t *testing.T) {
	root := moduleRoot(t)
	for _, pkg := range migrated {
		files, err := filepath.Glob(filepath.Join(root, pkg, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("%s: no Go files (%v)", pkg, err)
		}
		inHTTPX := pkg == "internal/httpx"
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			rel, _ := filepath.Rel(root, path)
			ast.Inspect(f, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.CallExpr:
					isError := isSel(n.Fun, "httpx", "Error")
					if id, ok := n.Fun.(*ast.Ident); ok && inHTTPX && id.Name == "Error" {
						isError = true
					}
					if isError && len(n.Args) == 4 && hasStringLit(n.Args[3]) {
						t.Errorf("%s: httpx.Error with a literal sentence; use httpx.Say and a catalog entry", rel)
					}
				case *ast.CompositeLit:
					if isSel(n.Type, "httpx", "ErrorDetail") || (inHTTPX && isIdent(n.Type, "ErrorDetail")) {
						for _, e := range n.Elts {
							if kv, ok := e.(*ast.KeyValueExpr); ok && isIdent(kv.Key, "Message") && hasStringLit(kv.Value) {
								t.Errorf("%s: ErrorDetail with a literal message; use httpx.Detail and a catalog entry", rel)
							}
						}
					}
				}
				return true
			})
		}
	}
}

func isSel(e ast.Expr, pkg, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == name && isIdent(sel.X, pkg)
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

// hasStringLit reports whether e holds a string literal anywhere: a
// literal, a concatenation, a fmt.Sprintf format.
func hasStringLit(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			found = true
		}
		return !found
	})
	return found
}

// walkGo parses every non-test Go file of the hub but this package.
func walkGo(t *testing.T, fn func(path string, f *ast.File)) {
	t.Helper()
	root := moduleRoot(t)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", ".claude", "dist", "web", "design", "spikes", "msg":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		fn(rel, f)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test")
		}
		dir = parent
	}
}
