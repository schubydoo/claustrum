package git

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestCreateRollbacksRetakeTheLock: each rollback of a create takes the create
// lock first, so no rollback runs during the staged add of another create of
// the repository (withWorktreeCreateLock, claustrum's own). The first statement
// of undoFailedAddLeaf, undoFailedCheckout and removeCreatedLeaf is
// `cp.lock.retake()`. Mutation: drop it from any one of them.
func TestCreateRollbacksRetakeTheLock(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "worktreeverify.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"undoFailedAddLeaf": false, "undoFailedCheckout": false, "removeCreatedLeaf": false}
	for _, d := range file.Decls {
		f, ok := d.(*ast.FuncDecl)
		if !ok || f.Recv != nil {
			continue
		}
		if _, ok := want[f.Name.Name]; !ok {
			continue
		}
		want[f.Name.Name] = len(f.Body.List) > 0 && isLockRetake(f.Body.List[0])
	}
	for name, ok := range want {
		if !ok {
			t.Errorf("%s does not start with cp.lock.retake()", name)
		}
	}
}

// isLockRetake reports whether s is the statement `cp.lock.retake()`.
func isLockRetake(s ast.Stmt) bool {
	e, ok := s.(*ast.ExprStmt)
	if !ok {
		return false
	}
	c, ok := e.X.(*ast.CallExpr)
	if !ok || len(c.Args) != 0 {
		return false
	}
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "retake" {
		return false
	}
	lock, ok := sel.X.(*ast.SelectorExpr)
	if !ok || lock.Sel.Name != "lock" {
		return false
	}
	cp, ok := lock.X.(*ast.Ident)
	return ok && cp.Name == "cp"
}
