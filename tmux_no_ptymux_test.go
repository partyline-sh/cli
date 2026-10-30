package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This name-based graph follows direct calls, including calls in closures. Selector calls
// conservatively visit local methods with that name, excluding llmsHome methods with
// colliding names (Render, Enter). Instead any reference to the llmsHome type in a
// reachable declaration fails. Receiver types hidden behind globals or interfaces cannot
// be distinguished for colliding method names. It cannot resolve arbitrary function values, interface
// dispatch, reflection, or commands launched in another process. Launcher callback
// initializers are included explicitly, and their named targets are also roots below.
func TestNoPtymuxInTmuxMode(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	funcs := map[string][]*ast.FuncDecl{}
	homes := map[*ast.FuncDecl]bool{}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			// The launcher invokes these variables after Bubble Tea exits. Include both
			// function literals and aliases such as launcherMerge = tmuxMerge.
			if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.VAR {
				for _, spec := range gen.Specs {
					value := spec.(*ast.ValueSpec)
					for i, id := range value.Names {
						if !strings.HasPrefix(id.Name, "launcher") || i >= len(value.Values) {
							continue
						}
						expr := value.Values[i]
						switch expr.(type) {
						case *ast.FuncLit, *ast.Ident:
							funcs[id.Name] = append(funcs[id.Name], &ast.FuncDecl{Name: ast.NewIdent(id.Name), Type: &ast.FuncType{Params: &ast.FieldList{}}, Body: &ast.BlockStmt{List: []ast.Stmt{
								&ast.ExprStmt{X: &ast.CallExpr{Fun: expr}},
							}}})
						}
					}
				}
			}
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			funcs[fn.Name.Name] = append(funcs[fn.Name.Name], fn)
			if fn.Recv != nil {
				typ := fn.Recv.List[0].Type
				if ptr, ok := typ.(*ast.StarExpr); ok {
					typ = ptr.X
				}
				if id, ok := typ.(*ast.Ident); ok && id.Name == "llmsHome" {
					homes[fn] = true
				}
			}
		}
	}
	var walk func(string, []string)
	seen := map[string]bool{}
	walk = func(name string, path []string) {
		path = append(append([]string(nil), path...), name)
		if seen[name] {
			return
		}
		seen[name] = true
		for _, fn := range funcs[name] {
			if homes[fn] {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && id.Name == "llmsHome" {
					t.Fatalf("reaches llmsHome: %s", strings.Join(path, " -> "))
				}
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				target := call.Fun
				switch generic := target.(type) {
				case *ast.IndexExpr:
					target = generic.X
				case *ast.IndexListExpr:
					target = generic.X
				}
				switch x := target.(type) {
				case *ast.Ident:
					walk(x.Name, path)
				case *ast.SelectorExpr:
					if id, ok := x.X.(*ast.Ident); ok && id.Name == "ptymux" && x.Sel.Name == "New" {
						t.Fatalf("reaches retired mux constructor: %s", strings.Join(path, " -> "))
					}
					onlyHome := len(funcs[x.Sel.Name]) > 0
					for _, method := range funcs[x.Sel.Name] {
						onlyHome = onlyHome && homes[method]
					}
					if onlyHome && x.Sel.Name != "Render" && x.Sel.Name != "Enter" {
						t.Fatalf("reaches llmsHome.%s: %s", x.Sel.Name, strings.Join(path, " -> "))
					}
					walk(x.Sel.Name, path)
				}
				return true
			})
		}
	}
	for _, root := range []string{"runTmuxApp", "runTmuxBare", "tmuxHome", "tmuxCmdMain", "runLauncherPopup", "finish", "launcherFocusSession", "newRunMenu", "launcherSplitSessions", "recoverWithResume", "tmuxNewWindow"} {
		if len(funcs[root]) == 0 {
			t.Fatalf("missing root %s", root)
		}
		walk(root, nil)
	}
}

func TestLoopNoRemovedCommandText(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && path != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, command := range []string{"approve", "decide", "stop"} {
			if strings.Contains(string(data), "ptln loop "+command) {
				t.Errorf("%s names removed command %s", path, command)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
