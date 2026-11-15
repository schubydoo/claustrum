package git

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestCreateRollbacksRetakeTheLock: in gitWorktreeCreateLocked each call of
// undoFailedCheckout and removeCreatedLeaf after the call of release follows a
// call of retake in the same block, and so does the call in the closure
// goneCheckout (withWorktreeCreateLock, claustrum's own). Mutation: drop any
// one retake() after the release, or the one in goneCheckout.
func TestCreateRollbacksRetakeTheLock(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "methods_git.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	for _, d := range file.Decls {
		if f, ok := d.(*ast.FuncDecl); ok && f.Name.Name == "gitWorktreeCreateLocked" {
			fn = f
		}
	}
	if fn == nil {
		t.Fatal("no gitWorktreeCreateLocked in methods_git.go")
	}
	// callOf looks at a simple statement only. A statement with a block of its
	// own is looked at in that block.
	callOf := func(s ast.Stmt, names ...string) bool {
		switch s.(type) {
		case *ast.AssignStmt, *ast.ExprStmt, *ast.ReturnStmt, *ast.DeclStmt:
		default:
			return false
		}
		found := false
		ast.Inspect(s, func(n ast.Node) bool {
			if _, ok := n.(*ast.FuncLit); ok {
				return false
			}
			if c, ok := n.(*ast.CallExpr); ok {
				if id, ok := c.Fun.(*ast.Ident); ok {
					for _, name := range names {
						found = found || id.Name == name
					}
				}
			}
			return true
		})
		return found
	}
	isCall := func(s ast.Stmt, name string) bool {
		e, ok := s.(*ast.ExprStmt)
		if !ok {
			return false
		}
		c, ok := e.X.(*ast.CallExpr)
		if !ok {
			return false
		}
		id, ok := c.Fun.(*ast.Ident)
		return ok && id.Name == name && len(c.Args) == 0
	}
	var released token.Pos
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if s, ok := n.(ast.Stmt); ok && released == token.NoPos && isCall(s, "release") {
			released = s.Pos()
		}
		return true
	})
	if released == token.NoPos {
		t.Fatal("no call of release in gitWorktreeCreateLocked")
	}
	checked := 0
	visit := func(list []ast.Stmt, inClosure bool) {
		for i, s := range list {
			if (inClosure || s.Pos() > released) && callOf(s, "undoFailedCheckout", "removeCreatedLeaf") {
				checked++
				if i == 0 || !isCall(list[i-1], "retake") {
					t.Errorf("%s: a rollback with no retake() right before it", fset.Position(s.Pos()))
				}
			}
		}
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			if id, ok := x.Lhs[0].(*ast.Ident); ok && id.Name == "goneCheckout" {
				lit := x.Rhs[0].(*ast.FuncLit)
				visit(lit.Body.List, true)
				return false
			}
		case *ast.FuncLit:
			return false
		case *ast.BlockStmt:
			visit(x.List, false)
		case *ast.CaseClause:
			visit(x.Body, false)
		}
		return true
	})
	// The instrument finds each rollback after the release and the one of
	// goneCheckout.
	if checked < 11 {
		t.Errorf("checked %d rollbacks, want at least 11", checked)
	}
}
