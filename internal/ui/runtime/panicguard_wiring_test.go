package runtime

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestRun_InstallsLoopPanicGuard is a STRUCTURAL check, deliberately so.
//
// Every other guard test exercises behavior, but none of them can reach the one
// line that arms the loop guard in production — `defer rt.recoverLoop()` in Run.
// Run cannot be called from a test at all: its first statement is
// term.MakeRaw(os.Stdin.Fd()), which fails without a TTY and returns before the
// defer is installed. Wiring a PTY in would mean taking on a new dependency
// (creack/pty is not a real dep here — go.sum carries only its go.mod hash) for
// a single assertion.
//
// So this parses the source instead and asserts the defer is present. Delete
// that line and every behavioral test still passes; this one fails. That is the
// specific regression it exists to catch, and it is what replaced the manual
// F12 harness.
//
// If Run is ever restructured so the guard moves (e.g. into loop()), update the
// wantIn/wantCall pair below rather than deleting the test.
func TestRun_InstallsLoopPanicGuard(t *testing.T) {
	t.Parallel()

	const (
		wantIn   = "Run"
		wantCall = "recoverLoop"
	)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "runtime.go", nil, 0)
	if err != nil {
		t.Fatalf("parse runtime.go: %v", err)
	}

	fn := findFuncDecl(file, wantIn)
	if fn == nil {
		t.Fatalf("func %s not found in runtime.go", wantIn)
	}

	if !hasDeferredCall(fn, wantCall) {
		t.Fatalf("%s does not install the loop panic guard: expected a "+
			"`defer rt.%s()` statement. Without it a panic escapes with the "+
			"terminal still in raw mode + alt-screen + mouse tracking, and the "+
			"trace never reaches the log file. See panicguard.go and "+
			"docs/dev/design/runtime.md.", wantIn, wantCall)
	}
}

func findFuncDecl(file *ast.File, name string) *ast.FuncDecl {
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

// hasDeferredCall reports whether fn contains `defer <anything>.name(...)`.
func hasDeferredCall(fn *ast.FuncDecl, name string) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		d, ok := n.(*ast.DeferStmt)
		if !ok {
			return true
		}
		if sel, ok := d.Call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
			found = true
		}
		return !found
	})
	return found
}
