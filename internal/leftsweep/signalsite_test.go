package leftsweep

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The signal guard of this package. Its production files signal only through
// procinfo.HoldPIDByFD, the pid file descriptor hold with no kill(pid) fallback. A
// selector that names another signal call fails the test (AGENTS.md Part A, the
// left-process sweep). The scan reads names only, with no type check.
func TestTheOnlySignalIsThePidfdHold(t *testing.T) {
	forbidden := map[string]bool{
		"Kill": true, "KillGroup": true, "Signal": true, "HoldPID": true,
		"Tgkill": true, "Tkill": true, "PidfdSendSignal": true, "Syscall": true,
		"Syscall6": true, "RawSyscall": true, "RawSyscall6": true,
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	holds := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if forbidden[sel.Sel.Name] {
				t.Errorf("%s: selector %s is a signal call outside the pidfd hold", name, sel.Sel.Name)
			}
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == "procinfo" && sel.Sel.Name == "HoldPIDByFD" {
				holds++
			}
			return true
		})
	}
	if holds != 1 {
		t.Errorf("procinfo.HoldPIDByFD appears %d times in the production files, want 1", holds)
	}
}
