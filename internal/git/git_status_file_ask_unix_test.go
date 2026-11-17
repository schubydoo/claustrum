//go:build unix

package git

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// git.status with a regular file inside a repository as path and baseRepo asks git
// about that repository in the first request: the start test, then `rev-parse
// --git-dir` at the root and the read of its config, then `version`. A failing ask
// answers its -32603 text.
func TestGitStatusFileBaseRepoAsksGit(t *testing.T) {
	f := newShadowFixture(t)
	now := setProbeClock(t)
	file := filepath.Join(f.repo, "README.ask")
	if err := os.WriteFile(file, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	params := map[string]any{"path": file, "baseRepo": file}
	raw, calls := f.call(t, "git.status", params)
	if raw != statusNotRepo {
		t.Errorf("frame = %s\nwant %s", raw, statusNotRepo)
	}
	var tail []string
	for _, c := range calls[max(len(calls)-3, 0):] {
		tail = append(tail, strings.Join(c.argv, " "))
	}
	want := []string{"-c core.hooksPath=/dev/null -c core.fsmonitor= rev-parse --git-dir", "config --no-includes --file - --list -z", "version"}
	if len(calls) != 14 || !slices.Equal(tail, want) {
		t.Errorf("%d calls ending %q, want 14 ending %q", len(calls), tail, want)
	}

	// After the kept answer is old, the next request asks again.
	*now = now.Add(probeKeep)
	slowGit(t, "core.hooksPath=/dev/null,core.fsmonitor=,rev-parse,--git-dir", "fail", 0, "fatal: injected", "")
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "128")
	raw, _ = f.call(t, "git.status", map[string]any{"path": file, "baseRepo": file})
	wantErr := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"git could not check this repository on the remote machine: at \"` + f.repo + `\": fatal: injected"}}`
	if raw != wantErr {
		t.Errorf("failing ask frame = %s\nwant %s", raw, wantErr)
	}
}
