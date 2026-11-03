package hostsweep

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/schubydoo/claustrum/internal/sockroute"
)

// noFolderHandle takes the /proc route away for one test.
func noFolderHandle(t *testing.T) {
	t.Helper()
	old := sockroute.SetProcSelfFDForTest(filepath.Join(t.TempDir(), "none"))
	t.Cleanup(func() { sockroute.SetProcSelfFDForTest(old) })
}

// helperCommand returns this test binary's path plus the env overlay that makes a
// child run it in the given helper mode.
func helperCommand(t *testing.T, mode string) (string, map[string]string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return exe, map[string]string{"CLAUSTRUM_TEST_HELPER": mode}
}

// buildEnv lays env over the environment of this process. os/exec keeps the last
// value of a name that appears twice.
func buildEnv(env map[string]string) []string {
	out := os.Environ()
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}
