package tests

// The routes the webapp serves, read from its sources, for the sweep of
// every route (projects_sweep_e2e_test.go). They are read from the patterns
// its files give mux.HandleFunc and mux.Handle rather than asked of the
// server, so the webapp keeps no registry for the tests' sake, and a route
// added is swept the moment it is written. A pattern is a string, or one
// built of strings: the methods of a loop over a list of them, a path held
// in a variable of the function, or one its callers pass from a loop over
// a map's keys. A pattern built any other way fails the test, saying where,
// so none goes unswept unnoticed.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Where a call is: its function, and the nodes from the function down to it.
type routeSite struct {
	fn    *ast.FuncDecl
	stack []ast.Node
	pos   token.Position
}

// The webapp's package, read for its routes.
type routeSources struct {
	fset *token.FileSet
	// Its functions by name, the calls of each function by name, and its
	// package-level variables and constants.
	funcs map[string]*ast.FuncDecl
	calls map[string][]routeSite
	vars  map[string]ast.Expr
	// Each call registering a route.
	routes []struct {
		site routeSite
		call *ast.CallExpr
	}
}

// Every route the webapp's mux registers, "METHOD /path" each, sorted;
// GET / is the static files', which isn't the API's.
func webappRoutes(t *testing.T) []string {
	t.Helper()
	src := readRouteSources(t, "..")
	seen := map[string]bool{}
	for _, r := range src.routes {
		patterns, err := src.values(r.call.Args[0], r.site, 0)
		if err != nil {
			t.Fatalf("%s: a route the sweep can't read: %v; say how its pattern is built in routes_test.go", r.site.pos, err)
		}
		for _, p := range patterns {
			if p != "GET /" {
				seen[p] = true
			}
		}
	}
	var out []string
	for p := range seen {
		out = append(out, p)
	}
	slices.Sort(out)
	if len(out) < 150 {
		t.Fatalf("read only %d routes from the webapp's sources", len(out))
	}
	return out
}

func readRouteSources(t *testing.T, dir string) *routeSources {
	t.Helper()
	src := &routeSources{fset: token.NewFileSet(), funcs: map[string]*ast.FuncDecl{}, calls: map[string][]routeSite{}, vars: map[string]ast.Expr{}}
	names, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(src.fset, name, data, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.GenDecl:
				for _, s := range d.Specs {
					if vs, ok := s.(*ast.ValueSpec); ok {
						for i, n := range vs.Names {
							if i < len(vs.Values) {
								src.vars[n.Name] = vs.Values[i]
							}
						}
					}
				}
			case *ast.FuncDecl:
				if d.Recv == nil {
					src.funcs[d.Name.Name] = d
				}
				src.walk(d)
			}
		}
	}
	return src
}

// Notes each call in fn: those registering a route, and those of the
// package's functions by name.
func (src *routeSources) walk(fn *ast.FuncDecl) {
	var stack []ast.Node
	ast.Inspect(fn, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		site := routeSite{fn: fn, stack: slices.Clone(stack), pos: src.fset.Position(call.Pos())}
		switch f := call.Fun.(type) {
		case *ast.SelectorExpr:
			if x, ok := f.X.(*ast.Ident); ok && x.Name == "mux" && (f.Sel.Name == "HandleFunc" || f.Sel.Name == "Handle") && len(call.Args) > 0 {
				src.routes = append(src.routes, struct {
					site routeSite
					call *ast.CallExpr
				}{site, call})
			}
		case *ast.Ident:
			src.calls[f.Name] = append(src.calls[f.Name], site)
		}
		return true
	})
}

// The strings expr may be where site is.
func (src *routeSources) values(expr ast.Expr, site routeSite, depth int) ([]string, error) {
	if depth > 8 {
		return nil, fmt.Errorf("%s is built too deep", src.text(expr))
	}
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return nil, fmt.Errorf("%s isn't a string", e.Value)
		}
		s, err := strconv.Unquote(e.Value)
		return []string{s}, err
	case *ast.ParenExpr:
		return src.values(e.X, site, depth+1)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			break
		}
		left, err := src.values(e.X, site, depth+1)
		if err != nil {
			return nil, err
		}
		right, err := src.values(e.Y, site, depth+1)
		if err != nil {
			return nil, err
		}
		var out []string
		for _, l := range left {
			for _, r := range right {
				out = append(out, l+r)
			}
		}
		return out, nil
	case *ast.Ident:
		return src.ident(e.Name, site, depth)
	case *ast.CompositeLit:
		var out []string
		for _, el := range e.Elts {
			v, err := src.values(el, site, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, v...)
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s is built in a way the sweep doesn't read", src.text(expr))
}

// The strings a name may hold where site is: the key or value of a loop
// around it, a variable the function sets, a parameter its callers pass,
// or a package-level variable or constant.
func (src *routeSources) ident(name string, site routeSite, depth int) ([]string, error) {
	for i := len(site.stack) - 1; i >= 0; i-- {
		rs, ok := site.stack[i].(*ast.RangeStmt)
		if !ok {
			continue
		}
		key, isKey := rs.Key.(*ast.Ident)
		value, isValue := rs.Value.(*ast.Ident)
		over := rs.X
		if id, ok := over.(*ast.Ident); ok {
			if v, ok := src.vars[id.Name]; ok {
				over = v
			}
		}
		lit, ok := over.(*ast.CompositeLit)
		switch {
		case isKey && key.Name == name:
			if !ok {
				return nil, fmt.Errorf("%s ranges over %s, which the sweep doesn't read", name, src.text(rs.X))
			}
			var keys []ast.Expr
			for _, el := range lit.Elts {
				if kv, ok := el.(*ast.KeyValueExpr); ok {
					keys = append(keys, kv.Key)
				}
			}
			if len(keys) == 0 {
				return nil, fmt.Errorf("%s is the index of %s", name, src.text(rs.X))
			}
			return src.values(&ast.CompositeLit{Elts: keys}, site, depth+1)
		case isValue && value.Name == name:
			if !ok {
				return nil, fmt.Errorf("%s ranges over %s, which the sweep doesn't read", name, src.text(rs.X))
			}
			var vals []ast.Expr
			for _, el := range lit.Elts {
				if kv, ok := el.(*ast.KeyValueExpr); ok {
					vals = append(vals, kv.Value)
				} else {
					vals = append(vals, el)
				}
			}
			return src.values(&ast.CompositeLit{Elts: vals}, site, depth+1)
		}
	}
	var assigned ast.Expr
	ast.Inspect(site.fn.Body, func(n ast.Node) bool {
		if as, ok := n.(*ast.AssignStmt); ok && as.Tok == token.DEFINE && len(as.Lhs) == len(as.Rhs) {
			for i, l := range as.Lhs {
				if id, ok := l.(*ast.Ident); ok && id.Name == name {
					assigned = as.Rhs[i]
				}
			}
		}
		return assigned == nil
	})
	if assigned != nil {
		return src.values(assigned, site, depth+1)
	}
	index := 0
	for _, field := range site.fn.Type.Params.List {
		for _, n := range field.Names {
			if n.Name == name {
				callers := src.calls[site.fn.Name.Name]
				if len(callers) == 0 {
					return nil, fmt.Errorf("%s is a parameter of %s, which nothing calls", name, site.fn.Name.Name)
				}
				var out []string
				for _, c := range callers {
					call := c.stack[len(c.stack)-1].(*ast.CallExpr)
					v, err := src.values(call.Args[index], c, depth+1)
					if err != nil {
						return nil, err
					}
					out = append(out, v...)
				}
				return out, nil
			}
			index++
		}
	}
	if v, ok := src.vars[name]; ok {
		return src.values(v, site, depth+1)
	}
	return nil, fmt.Errorf("%s holds what the sweep can't tell", name)
}

func (src *routeSources) text(e ast.Expr) string {
	start, end := src.fset.Position(e.Pos()), src.fset.Position(e.End())
	data, err := os.ReadFile(start.Filename)
	if err != nil || end.Offset > len(data) {
		return fmt.Sprintf("the expression at %s", start)
	}
	return string(data[start.Offset:end.Offset])
}

// The routes read are those the webapp serves: each kind of pattern is
// read, the loops over methods and the skills' and commands' routes too.
func TestWebappRoutesAreRead(t *testing.T) {
	routes := webappRoutes(t)
	for _, want := range []string{
		"GET /api/flows", "PATCH /api/processes/{id}", "POST /hooks/{token}",
		"GET /llm/{provider}/{path...}", "POST /llm/{provider}/{path...}",
		"DELETE /mcp/{id}", "OPTIONS /credentials/{id}/{path...}",
		"GET /api/skills", "PUT /api/commands/{id}/owner",
	} {
		if !slices.Contains(routes, want) {
			t.Errorf("the routes read lack %s: %q", want, routes)
		}
	}
}
