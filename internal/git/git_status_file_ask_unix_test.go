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

// git.status on a file link from a repository into another repository gets the
// gate of the target's repository (14 calls, as for a regular file there) and
// answers "no repository".
func TestGitStatusFileLinkIntoOtherRepoNotRepo(t *testing.T) {
	f := newShadowFixture(t)
	setProbeClock(t)
	other := filepath.Join(filepath.Dir(f.repo), "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	buildGit(t, other, "init", "-q")
	target := filepath.Join(other, "file")
	if err := os.WriteFile(target, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.repo, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	raw, calls := f.call(t, "git.status", map[string]any{"path": link, "baseRepo": link})
	if raw != statusNotRepo {
		t.Errorf("frame = %s\nwant %s", raw, statusNotRepo)
	}
	if len(calls) != 14 {
		t.Errorf("%d calls, want 14 (the gate of the target's repository)", len(calls))
	}
}

// git.status on a file link from a repository to a file outside any repository
// gets no gate: 2 calls, and no failing ask can change the reply.
func TestGitStatusFileLinkToPlainFolderNoGate(t *testing.T) {
	f := newShadowFixture(t)
	setProbeClock(t)
	plain := filepath.Join(filepath.Dir(f.repo), "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(plain, "file")
	if err := os.WriteFile(target, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.repo, "link2")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	slowGit(t, "core.hooksPath=/dev/null,core.fsmonitor=,rev-parse,--git-dir", "fail", 0, "fatal: injected", "")
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "128")
	raw, calls := f.call(t, "git.status", map[string]any{"path": link, "baseRepo": link})
	if raw != statusNotRepo {
		t.Errorf("frame = %s\nwant %s", raw, statusNotRepo)
	}
	if len(calls) != 2 {
		t.Errorf("%d calls, want 2", len(calls))
	}
}

// git.status on a file link from a folder outside any repository into a
// repository gets the gate of the target's folder: a failing ask answers its
// -32603 text at that repository.
func TestGitStatusFileLinkFromPlainFolderAsksGit(t *testing.T) {
	f := newShadowFixture(t)
	setProbeClock(t)
	plain := filepath.Join(filepath.Dir(f.repo), "plainq")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(f.repo, "README.into")
	if err := os.WriteFile(target, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(plain, "link3")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	slowGit(t, "core.hooksPath=/dev/null,core.fsmonitor=,rev-parse,--git-dir", "fail", 0, "fatal: injected", "")
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "128")
	raw, _ := f.call(t, "git.status", map[string]any{"path": link, "baseRepo": link})
	want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"git could not check this repository on the remote machine: at \"` + f.repo + `\": fatal: injected"}}`
	if raw != want {
		t.Errorf("frame = %s\nwant %s", raw, want)
	}
}

// git.status on a regular file inside a repository keeps the "disagree" text
// when git names another git directory, as a folder baseRepo does. Only a file
// link out of its folder answers "no repository".
func TestGitStatusRegularFileDisagreeKeepsError(t *testing.T) {
	f := newShadowFixture(t)
	setProbeClock(t)
	file := filepath.Join(f.repo, "README.disagree")
	if err := os.WriteFile(file, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	answerProbe(t, 0, "/elsewhere/.git\n", "")
	raw, _ := f.call(t, "git.status", map[string]any{"path": file, "baseRepo": file})
	want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"git and Claude disagree about which repository holds this folder: from \"` +
		f.repo + `\" git finds \"/elsewhere/.git\", Claude finds \"` + f.gitDir + `\"; try again"}}`
	if raw != want {
		t.Errorf("frame = %s\nwant %s", raw, want)
	}
}
