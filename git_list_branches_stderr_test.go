package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// git.list_branches must read git's STDOUT only. A repo holding a broken ref
// makes `for-each-ref` write a warning to stderr while still exiting 0 and
// printing the good branches on stdout, so a combined read turns that warning
// into a wire-visible branch name.
//
// Measured 2026-08-02 against the reference at 5db5e4a, with a clean-repo
// control that agreed:
//
//	reference : {"isRepo":true,"branches":["main","real"]}
//	claustrum : {"isRepo":true,"branches":["main","real",
//	             "warning: ignoring broken ref refs/heads/broken"]}
//
// Same defect gitStatusErr fixed for git.status (TestGitStatusIgnoresGitStderr),
// one function over. Cross-platform: the fixture is a file write, not a chmod,
// so this runs on all three CI legs — unlike the git.status version, which needs
// an unreadable directory and is therefore Unix-and-not-root only.
func TestGitListBranchesIgnoresGitStderr(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(cmd.Environ(),
			"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	run("commit", "-q", "--allow-empty", "-m", "init")
	run("branch", "real")

	// A ref file whose contents are not a valid object id. git warns and carries on.
	broken := filepath.Join(dir, ".git", "refs", "heads", "broken")
	if err := os.WriteFile(broken, []byte("this-is-not-an-object-id\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Confirm the fixture actually provokes the split this test depends on:
	// exit 0, branches on stdout, warning on stderr. Without this the assertion
	// below would pass on a git that simply never warns, and the test would
	// report the inverse of the truth.
	cmd := exec.Command("git", "-C", dir, "for-each-ref",
		"--format=%(refname:short)", "refs/heads")
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatalf("fixture: for-each-ref should still exit 0, got %v (stderr %q)", err, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "broken ref") {
		t.Skipf("this git does not warn about a broken ref (stderr %q); nothing to pin", errBuf.String())
	}
	if !strings.Contains(string(stdout), "real") {
		t.Fatalf("fixture: good branches should still reach stdout, got %q", stdout)
	}

	out, gerr := gitStdoutErr(dir, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if gerr != nil {
		t.Fatalf("gitStdoutErr reported failure on an exit-0 git: %v (%q)", gerr, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "warning:") {
			t.Errorf("git's stderr reached the branch list: %q", line)
		}
	}
	if !strings.Contains(out, "main") || !strings.Contains(out, "real") {
		t.Errorf("stdout-only read lost the real branches: %q", out)
	}
}

// The split between git() and gitStdoutErr is a per-caller decision, so pin that
// git() still returns COMBINED output — gitWorktreeCreate depends on it, and a
// well-meaning "consistency" change to Output() would silently empty that frame.
// TestSocketWorktreeCreateFailureCarriesGitStderr covers the frame itself; this
// covers the helper, so the reason survives even if that test is rewritten.
func TestGitHelperStillReadsCombinedOutput(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	// `git -C <dir> status` in a non-repo writes its fatal to stderr and exits
	// non-zero. git() must still hand that text back; gitStdoutErr must not.
	combined, okCombined := git(dir, "status", "--porcelain")
	stdoutOnly, errStdout := gitStdoutErr(dir, "status", "--porcelain")

	if okCombined || errStdout == nil {
		t.Fatalf("git in a non-repo should fail: combined ok=%v stdout err=%v", okCombined, errStdout)
	}
	if !strings.Contains(combined, "not a git repository") {
		t.Errorf("git() dropped git's stderr text: %q", combined)
	}
	if stdoutOnly != "" {
		t.Errorf("gitStdoutErr should carry no stderr text, got %q", stdoutOnly)
	}
}

// The helper test above pins gitStdoutErr. This one pins the CALL SITE, which is
// the line that actually puts the string on the wire.
//
// They are not interchangeable, and the distinction cost a round: with only the
// helper test present, reverting gitListBranches to git() left the entire suite
// green while shipping the defect verbatim. A test that exercises a helper says
// nothing about whether the helper is reached.
//
// Asserted as an exact slice, not a "warning:"-prefix scan, so any other stderr
// text leaking into branches[] fails here too.
func TestGitListBranchesMethodDropsGitStderr(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(cmd.Environ(),
			"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	run("commit", "-q", "--allow-empty", "-m", "init")
	run("branch", "real")
	if err := os.WriteFile(filepath.Join(dir, ".git", "refs", "heads", "broken"),
		[]byte("this-is-not-an-object-id\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Same fixture check as above: without the warning there is nothing to drop,
	// and a silent git would make this pass against the unfixed call site.
	cmd := exec.Command("git", "-C", dir, "for-each-ref",
		"--format=%(refname:short)", "refs/heads")
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	if _, err := cmd.Output(); err != nil {
		t.Fatalf("fixture: for-each-ref should exit 0, got %v", err)
	}
	if !strings.Contains(errBuf.String(), "broken ref") {
		t.Skipf("this git does not warn about a broken ref (stderr %q); nothing to pin", errBuf.String())
	}

	params, err := json.Marshal(map[string]any{"path": dir})
	if err != nil {
		t.Fatal(err)
	}
	resp := gitListBranches(&request{ID: json.RawMessage(`1`), Params: params})

	res, okType := resp.Result.(branchesResult)
	if !okType {
		t.Fatalf("git.list_branches result = %#v, want branchesResult", resp.Result)
	}
	want := []string{"main", "real"}
	if len(res.Branches) != len(want) {
		t.Fatalf("branches = %q, want exactly %q — git's stderr must not become a branch",
			res.Branches, want)
	}
	for i := range want {
		if res.Branches[i] != want[i] {
			t.Errorf("branches[%d] = %q, want %q", i, res.Branches[i], want[i])
		}
	}
}

// The OTHER frame this method gets wrong, and the one a stdout-only read alone
// does not fix. A corrupt packed-refs makes for-each-ref exit non-zero with
// nothing on stdout, and the reference answers -32603 carrying the Go error
// string — exactly as git.status does for the same class of failure.
//
// Measured 2026-08-02 against 5db5e4a:
//
//	reference : {"error":{"code":-32603,"message":"exit status 128"}}
//	before    : {"isRepo":true,"branches":["fatal: unexpected line in .git/packed-refs: …"]}
//	stdout-only alone: {"isRepo":true,"branches":[]}  — a different wrong answer
//
// That last line is why this test exists separately: the first fix looked
// complete and silently swapped one wrong frame for another.
func TestGitListBranchesPropagatesGitFailure(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(cmd.Environ(),
			"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	run("commit", "-q", "--allow-empty", "-m", "init")
	if err := os.WriteFile(filepath.Join(dir, ".git", "packed-refs"),
		[]byte("not a packed-refs file at all\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The fixture must make for-each-ref FAIL. If this git tolerates the file,
	// there is no failure to propagate and the assertions below are vacuous.
	cmd := exec.Command("git", "-C", dir, "for-each-ref",
		"--format=%(refname:short)", "refs/heads")
	if _, err := cmd.Output(); err == nil {
		t.Skip("this git does not fail on a corrupt packed-refs; nothing to pin")
	}

	params, err := json.Marshal(map[string]any{"path": dir})
	if err != nil {
		t.Fatal(err)
	}
	resp := gitListBranches(&request{ID: json.RawMessage(`1`), Params: params})

	if resp.Error == nil {
		t.Fatalf("want an error frame, got result %#v", resp.Result)
	}
	if resp.Error.Code != codeInternal {
		t.Errorf("error code = %d, want %d", resp.Error.Code, codeInternal)
	}
	// The Go error string, NOT git's "fatal: …" text — the reference reports the
	// exit status and keeps git's stderr off the wire entirely.
	if !strings.HasPrefix(resp.Error.Message, "exit status ") {
		t.Errorf("error message = %q, want the bare Go exit-status string", resp.Error.Message)
	}
	if strings.Contains(resp.Error.Message, "fatal:") {
		t.Errorf("git's stderr reached the error frame: %q", resp.Error.Message)
	}
}
