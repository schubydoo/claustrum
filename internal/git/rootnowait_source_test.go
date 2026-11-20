package git

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The reasons that an OpenRoot call of internal/git keeps the plain open.
const (
	rootKeepWindows = "Windows only: a folder there holds no FIFO"
	rootKeepDelete  = "a delete step of AGENTS.md Part A: a change there waits for the maintainer"
	rootKeepOutside = "not a folder of the daemon in TASK-587: not checked for a FIFO"
)

// plainOpenRootCalls lists each OpenRoot call of internal/git that does not go
// through openRootNoWait or openInRootNoWait. The key is the file, the
// function and the receiver kind ("os" or "root"). The value is the count and
// the reason.
var plainOpenRootCalls = map[string]struct {
	n      int
	reason string
}{
	"gitdirheld.go openRegistrations root":                       {1, rootKeepOutside},
	"gitdirtrust.go readCommondirAt os":                          {1, rootKeepOutside},
	"githarden.go statInsideDir os":                              {1, rootKeepOutside},
	"githarden_index_windows.go installTestedWorktreeIndex root": {1, rootKeepWindows},
	"gitrepoconfig.go gitDirNotFolderDetail os":                  {1, rootKeepOutside},
	"gitshadow.go removeEntry os":                                {1, rootKeepDelete},
	"gitshadow.go removeLink os":                                 {1, rootKeepDelete},
	"gitshadow_unix.go openHeldGitDir os":                        {1, rootKeepOutside},
	"gitshadow_windows.go openHeldGitDir os":                     {2, rootKeepWindows},
	"gitshadow_windows.go openHeldGitDir root":                   {1, rootKeepWindows},
	"gitshadowsweep.go sweepOldFolders os":                       {1, rootKeepDelete},
	"gitshadowuse.go worktreesOpenRefusal os":                    {1, rootKeepOutside},
	"gitshadowuse.go worktreesOpenRefusal root":                  {1, rootKeepOutside},
	"gitshadowuse.go makeWorktreesFolder os":                     {1, rootKeepOutside},
	"gitshadowuse.go replaceWorktreeGitFile os":                  {1, rootKeepOutside},
	"gitstatus.go statusMatchEntry os":                           {1, rootKeepOutside},
	"gitstatus.go presentSubmodules os":                          {1, rootKeepOutside},
	"gitworktreeconfig.go newWorktreeConfig os":                  {1, rootKeepOutside},
	"gitworktreeconfig.go openSourceLink os":                     {1, rootKeepOutside},
	"gitworktreeconfig.go writeWorktreeConfigAt os":              {1, rootKeepOutside},
	"gitworktreeconfig.go writeWorktreeConfigAt root":            {1, rootKeepOutside},
	"methods_git.go gitWorktreeRemoveLocked root":                {1, rootKeepDelete},
	"worktreecontent_linux.go leafLinkErr os":                    {1, rootKeepOutside},
	"worktreeexternal.go makeGoneWorktreeFolder os":              {1, rootKeepOutside},
	"worktreeremove.go externalRootDenied os":                    {1, rootKeepDelete},
	"worktreeremove.go openRemoveParent os":                      {1, rootKeepDelete},
	"worktreeremove.go openRemoveParent root":                    {1, rootKeepDelete},
	"worktreeremove.go deleteWorktreeDir root":                   {1, rootKeepDelete},
	// openAddStage opens u.gitDir only when gitDirMustBeReal is false, which is
	// Windows.
	"worktreestaging.go openAddStage os":                   {1, rootKeepWindows},
	"worktreestaging.go move root":                         {2, rootKeepOutside},
	"worktreestaging.go leafHoldsNoGitFile os":             {1, rootKeepOutside},
	"worktreestaging.go writeLeafGitFile os":               {1, rootKeepOutside},
	"worktreestale_windows.go removeStaleWorktreeEntry os": {1, rootKeepWindows},
	"worktreeverify.go clearWorktreeLeaf os":               {1, rootKeepDelete},
	"worktreeverify.go removeEmptyLeaf os":                 {1, rootKeepDelete},
}

// TestFoldersOfTheDaemonOpenWithNoWait pins that each OpenRoot call of
// internal/git goes through the open helpers, except the calls of
// plainOpenRootCalls. So a folder of the daemon that goes back to a plain
// os.OpenRoot, which waits at a FIFO, fails here.
func TestFoldersOfTheDaemonOpenWithNoWait(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]int{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || strings.HasPrefix(f, "rootnowait_") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range af.Decls {
			name := ""
			switch d := d.(type) {
			case *ast.FuncDecl:
				name = d.Name.Name
			case *ast.GenDecl:
				for _, s := range d.Specs {
					if v, ok := s.(*ast.ValueSpec); ok {
						for _, n := range v.Names {
							name += n.Name
						}
					}
				}
			}
			ast.Inspect(d, func(n ast.Node) bool {
				c, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				s, ok := c.Fun.(*ast.SelectorExpr)
				if !ok || s.Sel.Name != "OpenRoot" {
					return true
				}
				kind := "root"
				if id, ok := s.X.(*ast.Ident); ok && id.Name == "os" {
					kind = "os"
				}
				found[f+" "+name+" "+kind]++
				return true
			})
		}
	}
	for key, n := range found {
		if want := plainOpenRootCalls[key]; n != want.n {
			t.Errorf("%s: %d plain OpenRoot calls, want %d. Open a folder of the daemon with openRootNoWait or openInRootNoWait, or list the call with its reason", key, n, want.n)
		}
	}
	for key, want := range plainOpenRootCalls {
		if found[key] == 0 {
			t.Errorf("%s: listed with %d plain OpenRoot calls (%s), found none", key, want.n, want.reason)
		}
	}
}
