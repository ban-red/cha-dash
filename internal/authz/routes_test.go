package authz

import (
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

// registeredPatterns finds every ServeMux pattern the hub's code registers:
// the first argument of each .Handle / .HandleFunc call in the non-test Go
// files under cmd/, internal/ and integrations/. A pattern that isn't a
// string literal (or a constant expression of literals) is reported as an
// error, because nobody could check it against the table.
//
// internal/server's TestRouteMatrix then calls every row through the real
// router; this test is what catches a route that has no row at all.
func registeredPatterns(t *testing.T) map[string]string {
	t.Helper()
	root := filepath.Join("..", "..")
	found := map[string]string{} // pattern → file:line
	fset := token.NewFileSet()
	for _, dir := range []string{"cmd", "internal", "integrations"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !strings.Contains(string(src), ".Handle") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) != 2 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
					return true
				}
				pos := fset.Position(call.Pos())
				where := filepath.ToSlash(strings.TrimPrefix(pos.Filename, root+string(filepath.Separator))) + ":" + strconv.Itoa(pos.Line)
				p, ok := constString(call.Args[0], f)
				if ok && !looksLikePattern(p) {
					return true
				}
				if !ok && !isMux(sel.X) {
					return true // another Handle method (slog.Handler's, say)
				}
				if !ok {
					t.Errorf("%s: %s's pattern isn't a string literal, so it can't be checked against internal/authz/routes.go", where, sel.Sel.Name)
					return true
				}
				found[p] = where
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return found
}

// looksLikePattern: "/path" or "METHOD /path".
func looksLikePattern(s string) bool {
	if strings.HasPrefix(s, "/") {
		return true
	}
	method, path, ok := strings.Cut(s, " ")
	return ok && method != "" && strings.ToUpper(method) == method && strings.HasPrefix(path, "/")
}

// isMux reports whether a receiver is, by the codebase's convention, a
// ServeMux: a variable or field named mux (or ending in Mux).
func isMux(e ast.Expr) bool {
	var name string
	switch e := e.(type) {
	case *ast.Ident:
		name = e.Name
	case *ast.SelectorExpr:
		name = e.Sel.Name
	}
	return name == "mux" || strings.HasSuffix(name, "Mux")
}

// constString evaluates a string literal, a concatenation of them, or a
// package-level constant declared in the same file.
func constString(e ast.Expr, f *ast.File) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(e.Value)
		return s, err == nil
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		a, ok1 := constString(e.X, f)
		b, ok2 := constString(e.Y, f)
		return a + b, ok1 && ok2
	case *ast.ParenExpr:
		return constString(e.X, f)
	case *ast.Ident:
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.CONST {
				continue
			}
			for _, spec := range g.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if name.Name == e.Name && i < len(vs.Values) {
						return constString(vs.Values[i], f)
					}
				}
			}
		}
	}
	return "", false
}

// TestEveryRouteHasARow fails on any route registered without a row in
// Routes, and on any row (not marked Optional) for a route nobody registers.
func TestEveryRouteHasARow(t *testing.T) {
	found := registeredPatterns(t)
	if len(found) < 40 {
		t.Fatalf("found only %d patterns; the scan is broken", len(found))
	}
	for p, where := range found {
		if _, ok := Lookup(p); !ok {
			t.Errorf("%s registers %q, which has no row in internal/authz/routes.go: add one with the role it needs", where, p)
		}
	}
	for _, r := range Routes {
		if _, ok := found[r.Pattern]; !ok && !r.Optional {
			t.Errorf("routes.go lists %q, but nothing registers it: remove the row, or mark it Optional if another track adds it", r.Pattern)
		}
		if r.Access < Public || r.Access > NodeKey {
			t.Errorf("%q: access %d", r.Pattern, r.Access)
		}
	}
}

// TestNodeKeyRoutes: the node channel needs no session and no role admits
// it; it's the only such row.
func TestNodeKeyRoutes(t *testing.T) {
	for _, role := range []string{"", "household", "viewer", "operator", "admin", "owner"} {
		if NodeKey.Allows(role) {
			t.Errorf("NodeKey allows role %q", role)
		}
	}
	var keyed []string
	for _, r := range Routes {
		if r.Access == NodeKey {
			keyed = append(keyed, r.Pattern)
		}
	}
	if len(keyed) != 1 || keyed[0] != "GET /node" {
		t.Fatalf("node-key routes = %v", keyed)
	}
	if NodeKey.String() != "node key" {
		t.Fatal(NodeKey.String())
	}
}
