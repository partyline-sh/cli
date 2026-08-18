package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// THE RATCHET behind the whole setup effort: a failure a user cannot act on is the actual bug.
//
// Every diagnostic in setup and doctor takes a `fix` argument — the exact command to run. It is
// trivially easy to add a new check and pass "" for it, and the result reads fine in review while
// being useless in a terminal: the user learns something is wrong and nothing about what to do. That
// is precisely how "our system is too cryptic, fragile, requires tonnes of fucking around" happened.
//
// This test reads the SOURCE and fails on any failing-status diagnostic with an empty fix, so the
// rule cannot decay as checks are added. It deliberately does not run anything — a behavioural test
// would only cover the paths a fixture happens to reach, and the risk here is the path nobody
// thought about.
func TestEveryFailureNamesAFix(t *testing.T) {
	for _, file := range []string{"doctor.go", "setup.go", "daemon_doctor.go"} {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			status, isFailing := failingStatus(call)
			if !isFailing {
				return true
			}
			last := call.Args[len(call.Args)-1]
			if lit, ok := last.(*ast.BasicLit); ok && lit.Kind == token.STRING &&
				(lit.Value == `""` || lit.Value == "``") {
				t.Errorf("%s:%d: a %s diagnostic with no fix — the user is told something is wrong "+
					"and nothing about what to do. Pass the exact command that fixes it.",
					file, fset.Position(call.Pos()).Line, status)
			}
			return true
		})
	}
}

// failingStatus recognises the two spellings a diagnostic takes: `ckFail.line(what, detail, fix)`
// and `report(ckFail, what, detail, fix)`. ckPass is exempt — a passing check has nothing to fix,
// and demanding one would make the rule noise that gets suppressed rather than obeyed.
func failingStatus(call *ast.CallExpr) (string, bool) {
	name := func(e ast.Expr) string {
		id, ok := e.(*ast.Ident)
		if !ok {
			return ""
		}
		return id.Name
	}
	failing := func(s string) bool { return s == "ckFail" || s == "ckWarn" }

	if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "line" {
		if s := name(sel.X); failing(s) {
			return s, true
		}
		return "", false
	}
	if name(call.Fun) == "report" && len(call.Args) >= 4 {
		if s := name(call.Args[0]); failing(s) {
			return s, true
		}
	}
	return "", false
}

// The rule is only worth having if it can actually fail. A checker that silently matches nothing
// passes forever and protects nothing — so assert it recognises both spellings, and that ckPass is
// genuinely exempt rather than accidentally unmatched.
func TestTheFixRuleCanStillFail(t *testing.T) {
	src := `package p
func f() {
	ckFail.line("a", "b", "")
	ckWarn.line("a", "b", "")
	report(ckFail, "a", "b", "")
	ckPass.line("a", "b", "")
	report(ckPass, "a", "b", "")
}`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var caught []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if s, failing := failingStatus(call); failing {
			caught = append(caught, s)
		}
		return true
	})
	if got := strings.Join(caught, ","); got != "ckFail,ckWarn,ckFail" {
		t.Errorf("the checker matched %q — it must catch both spellings and exempt ckPass", got)
	}
}
