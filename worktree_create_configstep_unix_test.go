//go:build unix

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// dispatchErr is dispatchRaw for a goroutine that is not the test: it reports
// a failure as an error and calls no method of t.
func dispatchErr(s *server, line string) (string, error) {
	resp := s.dispatch(nil, []byte(line))
	if resp == nil {
		return "", errors.New("no reply")
	}
	b, err := json.Marshal(*resp)
	return string(b), err
}

// createWithin is create with a deadline of the test. The request runs in its
// own goroutine. A request that has no reply by then fails the test. A
// cleanup gives each open of a FIFO at the file of the repository a writer
// until the request returns, for at most 10 s, so that no git call outlives
// the test.
func (f configStepFixture) createWithin(t *testing.T, d time.Duration) (string, []envCall) {
	t.Helper()
	if err := os.WriteFile(f.log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	line := rpcLine(t, "git.worktree_create", f.params())
	type reply struct {
		raw string
		err error
	}
	done := make(chan reply, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		raw, err := dispatchErr(f.s, line)
		done <- reply{raw, err}
	}()
	t.Cleanup(func() {
		for end := time.Now().Add(10 * time.Second); time.Now().Before(end); {
			if w, err := os.OpenFile(f.main, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				_ = w.Close()
			}
			select {
			case <-finished:
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
		t.Error("the request did not return after the FIFO got writers for 10 s")
	})
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("the request: %v", r.err)
		}
		return r.raw, readEnvCalls(t, f.log)
	case <-time.After(d):
		t.Fatalf("no reply in %v: the request waits", d)
		return "", nil
	}
}

// fifoAt puts a FIFO in place of the file at p. It runs inside a request, so
// it reports a failure with t.Error only.
func fifoAt(t *testing.T, p string) {
	if err := os.Remove(p); err != nil {
		t.Error(err)
	}
	if err := syscall.Mkfifo(p, 0o644); err != nil {
		t.Error(err)
	}
}

// writeIn writes text to p inside a request, with t.Error for a failure.
func writeIn(t *testing.T, p, text string) {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Error(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Error(err)
	}
}

// TestConfigStepFailureFailsTheCreate: a failure of the step of the
// `config.worktree` file fails the create with the prefix and its cause. No
// read-tree runs, and the undo of a failed checkout runs. The rows are cells
// C2, C6, C7, C9, C10 and C11 of round task007-01 (Linux and macOS VMs). The
// stub or the seam after the add stands in for the wrapper of the round. In
// C9 and C11 the branch step reads the config again, gets its refusal and
// keeps the branch. Row C1 is TestConfigStepUnsetAllFailureFailsTheCreate.
func TestConfigStepFailureFailsTheCreate(t *testing.T) {
	const filemode = "[core]\n\tfilemode = false\n"
	const bare = "[core]\n\tbare = false\n"
	kept := func(f configStepFixture, cause string) string {
		return "; and the undo could not finish for " + f.leaf + ": branch w1 still exists; delete it by hand (for example: " +
			"git branch -d w1, which git refuses if that would lose commits) before retrying this branch name: " +
			"could not check whether another branch or remote-tracking ref reaches its commits (" + cause + ")"
	}
	for _, row := range []struct {
		cell  string
		text  string
		stage func(t *testing.T, f configStepFixture)
		// want is the error text after the prefix.
		want func(f configStepFixture) string
		// regKept and branchKept are what stays after the reply. The leaf goes in
		// every row.
		regKept, branchKept bool
	}{
		{"C2 the --unset-all call is ended at its bound", bare, func(t *testing.T, f configStepFixture) {
			old := shadowCallBound
			t.Cleanup(func() { shadowCallBound = old })
			shadowCallBound = 3 * time.Second
			t.Setenv("CLAUSTRUM_GITSTUB_MATCH2", "--unset-all,core.bare")
			t.Setenv("CLAUSTRUM_GITSTUB_MS2", "60000")
		}, func(configStepFixture) string {
			return "removing core.bare from the new worktree's config.worktree: signal: killed"
		}, false, false},
		{"C6 a folder with content at the destination", filemode, func(t *testing.T, f configStepFixture) {
			atStep(t, "added", func() { writeIn(t, filepath.Join(f.reg, worktreeConfigName, "f"), "y\n") })
		}, func(configStepFixture) string {
			return "removeat config.worktree: directory not empty"
		}, false, false},
		{"C7 a registration folder that is read-only", filemode, func(t *testing.T, f configStepFixture) {
			if os.Geteuid() == 0 {
				t.Skip("root writes into a read-only folder")
			}
			atStep(t, "added", func() {
				if err := os.Chmod(f.reg, 0o555); err != nil {
					t.Error(err)
				}
			})
			t.Cleanup(func() { _ = os.Chmod(f.reg, 0o755) })
		}, func(f configStepFixture) string {
			return "openat config.worktree: permission denied; and the undo could not finish for " + f.leaf +
				": the worktree registration and the branch remain; remove them by hand before retrying (RemoveAll w1: permission denied)"
		}, true, true},
		{"C9 a FIFO in place of the source after the add", filemode, func(t *testing.T, f configStepFixture) {
			atStep(t, "added", func() { fifoAt(t, f.main) })
		}, func(f configStepFixture) string {
			cause := repoConfigPrefix + `"` + f.main + `" is not a regular file`
			return cause + kept(f, cause)
		}, false, true},
		{"C10 a link to a file outside the git directory", filemode, func(t *testing.T, f configStepFixture) {
			outside := filepath.Join(filepath.Dir(f.repo), "outside-cw")
			mustWrite(t, outside, filemode)
			atStep(t, "added", func() {
				if err := os.Remove(f.main); err != nil {
					t.Error(err)
				}
				if err := os.Symlink(outside, f.main); err != nil {
					t.Error(err)
				}
			})
		}, func(f configStepFixture) string {
			return repoConfigPrefix + `"` + f.main + `": it is a link that is absolute, or does not lead to a regular file inside the git directory; replace it with a copy of the file`
		}, false, false},
		{"C11 a source rewritten broken after the add", filemode, func(t *testing.T, f configStepFixture) {
			atStep(t, "added", func() { writeIn(t, f.main, "[core]\n\tbare = false\n[broken\n") })
		}, func(f configStepFixture) string {
			cause := repoConfigPrefix + `fatal: bad config line 3 in "` + f.main + `"`
			return cause + kept(f, cause)
		}, false, true},
	} {
		t.Run(row.cell, func(t *testing.T) {
			f := newConfigStepFixture(t, row.text)
			row.stage(t, f)
			raw, calls := f.createWithin(t, 20*time.Second)
			if want := failedFrame(t, completingAddPrefix+row.want(f)); raw != want {
				t.Fatalf("git.worktree_create = %s\nwant %s", raw, want)
			}
			if ranReadTree(calls) {
				t.Error("a read-tree call ran, want none")
			}
			if exists(f.leaf) {
				t.Error("the leaf stays, want it gone")
			}
			if got := exists(f.reg); got != row.regKept {
				t.Errorf("the registration is there: %v, want %v", got, row.regKept)
			}
			if got := branchExists(t, f.repo, "w1"); got != row.branchKept {
				t.Errorf("the branch is there: %v, want %v", got, row.branchKept)
			}
		})
	}
}

// itemOn reports whether the call c carries the item `-c <item>`.
func itemOn(c envCall, item string) bool {
	for i := 0; i+1 < len(c.argv); i++ {
		if c.argv[i] == "-c" && c.argv[i+1] == item {
			return true
		}
	}
	return false
}

// TestConfigStepOfALinkedBase: with a linked worktree as baseRepo the copy
// comes from the file of the entry of that worktree. With no such file the
// new worktree gets none (cells C12 and C12b of round task007-01, Linux and
// macOS VMs). The `-c` items of the `worktree add` call and of the read-tree
// come from that file too, and none from the file of the repository (cells
// C12 and C12b of round task007-02, macOS VM).
func TestConfigStepOfALinkedBase(t *testing.T) {
	for _, row := range []struct {
		cell, own string
	}{
		{"C12 the base has its own file", "[core]\n\teol = lf\n"},
		{"C12b the base has no own file", ""},
	} {
		t.Run(row.cell, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			dropFixtureWorktree(t, f)
			enableWorktreeConfig(t, f, "[core]\n\tfilemode = false\n")
			base := filepath.Join(resolveTestRoot(t, t.TempDir()), "w0")
			runGit(t, f.repo, "worktree", "add", "-q", base, "-b", "b0")
			// git gives the new entry w0 a copy of the file of the repository.
			own := filepath.Join(f.gitDir, worktreesSubdir, "w0", worktreeConfigName)
			if row.own == "" {
				if err := os.Remove(own); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			} else {
				mustWrite(t, own, row.own)
			}
			leaf := filepath.Join(base, ".claude", "worktrees", "w1")
			raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": base, "branchName": "w1", "worktreePath": leaf})
			if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"path":` + jsonQuoted(t, leaf) + `,"sourceBranch":"b0","branch":"w1"}}`; raw != want {
				t.Fatalf("git.worktree_create = %s\nwant %s", raw, want)
			}
			for _, kind := range []string{"add", "read-tree"} {
				i := slices.IndexFunc(calls, func(c envCall) bool { return slices.Contains(c.argv, kind) })
				if i < 0 {
					t.Fatalf("no %s call", kind)
				}
				if got, want := itemOn(calls[i], "core.eol=lf"), row.own != ""; got != want {
					t.Errorf("the %s call has -c core.eol=lf: %v, want %v", kind, got, want)
				}
				if itemOn(calls[i], "core.filemode=false") {
					t.Errorf("the %s call has -c core.filemode=false of the file of the repository", kind)
				}
			}
			placed := filepath.Join(f.gitDir, worktreesSubdir, "w1", worktreeConfigName)
			if row.own == "" {
				if exists(placed) {
					t.Errorf("the new registration has the file %q, want none", mustRead(t, placed))
				}
				return
			}
			if got := mustRead(t, placed); got != row.own {
				t.Errorf("the file of the new registration = %q, want %q", got, row.own)
			}
		})
	}
}

// TestFIFOConfigWorktreeRefusedBeforeTheAdd: a FIFO at the `config.worktree`
// file of the repository refuses the create before the add, and nothing is
// made (cell C8 of round task007-01, Linux and macOS VMs). The deadline of the
// test is short: without the refusal the request does not answer.
func TestFIFOConfigWorktreeRefusedBeforeTheAdd(t *testing.T) {
	f := newConfigStepFixture(t, "[core]\n\tfilemode = false\n")
	fifoAt(t, f.main)
	raw, calls := f.createWithin(t, 20*time.Second)
	if want := failedFrame(t, repoConfigPrefix+`"`+f.main+`" is not a regular file`); raw != want {
		t.Fatalf("git.worktree_create = %s\nwant %s", raw, want)
	}
	if slices.ContainsFunc(calls, func(c envCall) bool { return slices.Contains(c.argv, "worktree") }) {
		t.Error("a worktree call ran, want none")
	}
	for _, p := range []string{f.leaf, f.reg} {
		if exists(p) {
			t.Errorf("%s is there, want nothing made", p)
		}
	}
	if branchExists(t, f.repo, "w1") {
		t.Error("the branch w1 is there, want none")
	}
}

// TestGateRefusesAnUnusableConfigWorktree: a `config.worktree` file of the
// repository that cannot be opened or is over 8 MiB is refused at the gate, on
// git.worktree_create and on git.info. The create runs no add and makes no
// folder `worktrees` (cells R4 and R7 of round task007-03, Linux and macOS
// VMs).
func TestGateRefusesAnUnusableConfigWorktree(t *testing.T) {
	for _, row := range []struct {
		cell  string
		stage func(t *testing.T, path string)
		want  func(path string) string
	}{
		{"R4 a file of mode 000", func(t *testing.T, path string) {
			if os.Geteuid() == 0 {
				t.Skip("root opens a file of mode 000")
			}
			if err := os.Chmod(path, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
		}, func(path string) string { return repoConfigPrefix + "open " + path + ": permission denied" }},
		{"R7 a file of 9 MiB", func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("[core]\n\tfilemode = false\n# "+strings.Repeat("x", 9<<20)+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, func(path string) string { return repoConfigPrefix + `"` + path + `" is larger than 8 MiB` }},
	} {
		t.Run(row.cell, func(t *testing.T) {
			f := newConfigStepFixture(t, "[core]\n\tfilemode = false\n")
			row.stage(t, f.main)
			raw, calls := f.create(t)
			if want := failedFrame(t, row.want(f.main)); raw != want {
				t.Fatalf("git.worktree_create = %s\nwant %s", raw, want)
			}
			if slices.ContainsFunc(calls, func(c envCall) bool { return slices.Contains(c.argv, "worktree") }) {
				t.Error("a worktree call ran, want none")
			}
			for _, p := range []string{f.leaf, filepath.Join(f.gitDir, worktreesSubdir)} {
				if exists(p) {
					t.Errorf("%s is there, want nothing made", p)
				}
			}
			if branchExists(t, f.repo, "w1") {
				t.Error("the branch w1 is there, want none")
			}
			info, _ := f.info(t, f.repo, nil)
			if want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":` + jsonQuoted(t, row.want(f.main)) + `}}`; info != want {
				t.Errorf("git.info = %s\nwant %s", info, want)
			}
		})
	}
}
