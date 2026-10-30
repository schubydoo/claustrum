//go:build unix

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The trust-check rules whose fixtures need a FIFO, a symlink, a permission bit or a
// file name Windows cannot hold. Measured side by side against f6010b97 on Linux and
// macOS VMs.

// fifoDeadline bounds each call that meets a FIFO.
const fifoDeadline = 10 * time.Second

// dispatchWithin sends one request and fails the test when the reply takes longer than
// fifoDeadline. Only the daemon's dispatch runs on the other goroutine, so it never
// touches t. Past the deadline the FIFO is opened for writing, without blocking, until
// the call returns. Each open releases one reader, and a method can open the FIFO
// more than once. The test then fails.
func dispatchWithin(t *testing.T, fifo, method string, params map[string]any) rpcReply {
	t.Helper()
	s := newTestServer(t)
	line := []byte(rpcLine(t, method, params))
	done := make(chan *response, 1)
	go func() { done <- s.dispatch(nil, line) }()
	var resp *response
	late := false
	deadline := time.After(fifoDeadline)
wait:
	for {
		select {
		case resp = <-done:
			break wait
		case <-deadline:
			late = true
		case <-time.After(20 * time.Millisecond):
			if late {
				if f, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
					_ = f.Close()
				}
			}
		}
	}
	if late {
		t.Fatalf("%s blocked on the FIFO %s for more than %s", method, fifo, fifoDeadline)
	}
	if resp == nil {
		t.Fatalf("%s: no reply", method)
	}
	raw, err := json.Marshal(*resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var r rpcReply
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("%s: undecodable reply %s: %v", method, raw, err)
	}
	return r
}

// shapeAsGitRepo gives dir a `.git` directory that passes the git-directory test, so the
// trust check lets git run there. The unix tests that stub git itself use it.
func shapeAsGitRepo(t *testing.T, dir string) {
	t.Helper()
	g := filepath.Join(dir, ".git")
	for _, d := range []string{"objects", "refs"} {
		if err := os.MkdirAll(filepath.Join(g, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(g, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mkfifo(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(p, 0o644); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, p string) {
	t.Helper()
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
}

// commondir with a symlink, a FIFO and a permission bit. It is read through a
// symlink only when the link is relative and stays inside the entry. A FIFO is refused
// at once. Every other case is refused with M4 and the reason text.
func TestGitDirTrustCommondirUnixKinds(t *testing.T) {
	t.Run("relative symlink inside the entry passes", func(t *testing.T) {
		r := newTrustRepo(t)
		cd := filepath.Join(r.entry(), "commondir")
		if err := os.Rename(cd, cd+".real"); err != nil {
			t.Fatal(err)
		}
		symlink(t, "commondir.real", cd)
		wantResultPrefix(t, "info(TW)", info(t, r.TW), `{"isRepo":true`)
	})
	for _, tc := range []struct {
		name   string
		make   func(t *testing.T, r trustRepo, cd string)
		reason string
	}{
		{"relative symlink leaving the entry", func(t *testing.T, r trustRepo, cd string) {
			writeFile(t, filepath.Join(r.entry(), "..", "outside"), "../..\n", 0o644)
			symlink(t, "../outside", cd)
		}, "openat commondir: path escapes from parent"},
		{"absolute symlink to a file inside the entry", func(t *testing.T, r trustRepo, cd string) {
			real := filepath.Join(r.entry(), "commondir.real")
			writeFile(t, real, "../..\n", 0o644)
			symlink(t, real, cd)
		}, "openat commondir: path escapes from parent"},
		{"dangling symlink", func(t *testing.T, _ trustRepo, cd string) {
			symlink(t, "nonexistent-target", cd)
		}, "openat commondir: no such file or directory"},
		{"FIFO", func(t *testing.T, _ trustRepo, cd string) { mkfifo(t, cd) }, "commondir is not a regular file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTrustRepo(t)
			cd := filepath.Join(r.entry(), "commondir")
			if err := os.Remove(cd); err != nil {
				t.Fatal(err)
			}
			tc.make(t, r, cd)
			m4 := wantM4(cd, tc.reason)
			wantRPCError(t, "info(TW)", dispatchWithin(t, cd, "git.info", map[string]any{"path": r.TW}), m4)
			wantRPCError(t, "list_branches(TW)", dispatchWithin(t, cd, "git.list_branches", map[string]any{"path": r.TW}), m4)
			checkRemoveRefused(t, dispatchWithin(t, cd, "git.worktree_remove", map[string]any{
				"baseRepo": r.TW, "worktreePath": r.SWW, "branchName": "s1"}), r.SWW, "s1", r.T)
		})
	}
	t.Run("unreadable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a mode-000 file")
		}
		r := newTrustRepo(t)
		cd := filepath.Join(r.entry(), "commondir")
		if err := os.Chmod(cd, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(cd, 0o644) })
		wantRPCError(t, "info(TW)", info(t, r.TW), wantM4(cd, "openat commondir: permission denied"))
	})
}

// On Linux and macOS no symlink in the commondir value is resolved, so naming
// the repository through an alias is refused. A backslash is an ordinary character.
func TestGitDirTrustCommondirUnixSpelling(t *testing.T) {
	t.Run("own repository through a symlink alias", func(t *testing.T) {
		r := newTrustRepo(t)
		alias := filepath.Join(r.base, "L")
		symlink(t, r.T, alias)
		cd := filepath.Join(r.entry(), "commondir")
		writeFile(t, cd, filepath.Join(alias, ".git")+"\n", 0o644)
		wantRPCError(t, "info(TW)", info(t, r.TW), wantM2(cd, filepath.Join(r.T, ".git")))
	})
	t.Run("backslashes", func(t *testing.T) {
		r := newTrustRepo(t)
		cd := filepath.Join(r.entry(), "commondir")
		writeFile(t, cd, `..\..`+"\n", 0o644)
		wantRPCError(t, "info(TW)", info(t, r.TW), wantM2(cd, filepath.Join(r.T, ".git")))
	})
}

// On Linux and macOS an entry replaced by a regular file means no repository.
func TestGitDirTrustEntryIsAFile(t *testing.T) {
	r := newTrustRepo(t)
	if err := os.RemoveAll(r.entry()); err != nil {
		t.Fatal(err)
	}
	writeFile(t, r.entry(), "x", 0o644)
	wantResult(t, "info(TW)", info(t, r.TW), notRepoInfo)
	wantResult(t, "list_branches(TW)", listBranches(t, r.TW), notRepoList)
}

// A HEAD that is a symlink or a FIFO, seen through a nested `.git`
// directory. A symlink passes only when its target text starts with "refs/". A FIFO
// fails at once, where git blocks on it.
func TestGitDirTrustHeadUnixKinds(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func(t *testing.T, g string)
		pass bool
	}{
		{"symlink to refs/heads/main", func(t *testing.T, g string) { symlink(t, "refs/heads/main", filepath.Join(g, "HEAD")) }, true},
		{"symlink to refs/../HEAD.real", func(t *testing.T, g string) {
			writeFile(t, filepath.Join(g, "HEAD.real"), "ref: refs/heads/main\n", 0o644)
			symlink(t, "refs/../HEAD.real", filepath.Join(g, "HEAD"))
		}, true},
		{"symlink to another file", func(t *testing.T, g string) {
			writeFile(t, filepath.Join(g, "HEAD.real"), "ref: refs/heads/main\n", 0o644)
			symlink(t, "HEAD.real", filepath.Join(g, "HEAD"))
		}, false},
		{"absolute symlink into refs", func(t *testing.T, g string) {
			writeFile(t, filepath.Join(g, "refs", "heads", "main"), "", 0o644)
			symlink(t, filepath.Join(g, "refs", "heads", "main"), filepath.Join(g, "HEAD"))
		}, false},
		{"FIFO", func(t *testing.T, g string) { mkfifo(t, filepath.Join(g, "HEAD")) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTrustRepo(t)
			a := filepath.Join(r.T, "a")
			g := filepath.Join(a, ".git")
			for _, d := range []string{"objects", "refs"} {
				if err := os.MkdirAll(filepath.Join(g, d), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			tc.make(t, g)
			head := filepath.Join(g, "HEAD")
			rep := dispatchWithin(t, head, "git.list_branches", map[string]any{"path": a})
			if tc.pass {
				wantResult(t, "list_branches(T/a)", rep, `{"isRepo":true,"branches":[]}`)
				return
			}
			wantResult(t, "list_branches(T/a)", rep, notRepoList)
			r1 := dispatchWithin(t, head, "git.worktree_create", map[string]any{
				"baseRepo": a, "worktreePath": filepath.Join(a, ".claude", "worktrees", "w1"), "branchName": "w1"})
			wantResult(t, "create(T/a)", r1, notRepoCreate)
		})
	}
}

// A main HEAD made a symlink to another file makes the main git directory fail the
// test. The linked worktree's entry is then not an entry, and its commondir "../.."
// gets the text of a damaged repository (claustrum's own for a HEAD, not measured).
// T itself is then no repository, so remove from T is refused.
func TestGitDirTrustMainHeadSymlinkRefusesEntry(t *testing.T) {
	r := newTrustRepo(t)
	head := filepath.Join(r.T, ".git", "HEAD")
	if err := os.Rename(head, head+".real"); err != nil {
		t.Fatal(err)
	}
	symlink(t, "HEAD.real", head)
	m1 := wantDamaged(filepath.Join(r.T, ".git"))
	wantRPCError(t, "info(TW)", info(t, r.TW), m1)
	wantCreateRefused(t, r.TW, r.T, m1)
	wantRemoveRefused(t, r.T, r.D, "d0", r.T)
}

// A `.git` FIFO is skipped and the walk goes on to the outer repository, at
// once. Git itself treats the FIFO by its version: git 2.47 also skips it, and a
// newer git refuses it with "not a regular file". The daemon passes that text
// through the hooks refusal. The trust check must not refuse the FIFO either way.
func TestGitDirTrustDotGitFifoIsSkipped(t *testing.T) {
	r := newTrustRepo(t)
	a := filepath.Join(r.T, "a")
	fifo := filepath.Join(a, ".git")
	mkfifo(t, fifo)
	rep := dispatchWithin(t, fifo, "git.info", map[string]any{"path": a})
	if rep.Error != nil && strings.HasPrefix(rep.Error.Message, "config-defined hooks could not be pinned off; git not run: ") &&
		strings.Contains(rep.Error.Message, "fatal: not a regular file: ") {
		return
	}
	wantResultPrefix(t, "info(T/a)", rep, `{"isRepo":true,"repo":"T"`)
}

// With symlinks: the walk starts at the resolved request directory, so a
// refusal names the resolved path, never the alias. A `.git` symlink to a gitdir file
// elsewhere works.
func TestGitDirTrustSymlinkedPaths(t *testing.T) {
	t.Run("request path through an alias", func(t *testing.T) {
		r := newTrustRepo(t)
		alias := filepath.Join(r.base, "L")
		symlink(t, r.T, alias)
		cd := filepath.Join(r.T, ".git", "commondir")
		writeFile(t, cd, "x\n", 0o644)
		wantRPCError(t, "info(L)", info(t, alias), wantTR(cd, "x"))
		wantCreateRefused(t, alias, r.T, wantTR(cd, "x"))
	})
	t.Run("alias to a subdirectory walks the resolved parents", func(t *testing.T) {
		// L points at T/sub. Walking up from the alias itself reaches base, which holds
		// no repository. Walking up from the resolved T/sub reaches T.
		r := newTrustRepo(t)
		sub := filepath.Join(r.T, "sub", "deep")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		alias := filepath.Join(r.base, "L")
		symlink(t, filepath.Join(r.T, "sub"), alias)
		cd := filepath.Join(r.T, ".git", "commondir")
		writeFile(t, cd, "x\n", 0o644)
		wantRPCError(t, "info(L/deep)", info(t, filepath.Join(alias, "deep")), wantTR(cd, "x"))
	})
	t.Run(".git symlink to a gitdir file", func(t *testing.T) {
		r := newTrustRepo(t)
		dotGit := filepath.Join(r.TW, ".git")
		moved := filepath.Join(r.base, "tw-gitfile")
		if err := os.Rename(dotGit, moved); err != nil {
			t.Fatal(err)
		}
		symlink(t, moved, dotGit)
		wantResultPrefix(t, "info(TW)", info(t, r.TW), `{"isRepo":true,"repo":"T_wt"`)
	})
}

// A remove named through a symlinked root still deletes the entry. Git records the
// worktree's resolved path in the entry, and the daemon resolves the path it was
// given through its nearest existing parent. This is the
// macOS /tmp -> /private/tmp case, measured against f6010b97 on a macOS VM, where the
// reference deletes the entry.
func TestGitDirTrustRemoveThroughSymlinkedRootPrunesEntry(t *testing.T) {
	base := realTempDir(t)
	T := filepath.Join(base, "T")
	shapeAsGitRepo(t, T)
	entry := filepath.Join(T, ".git", "worktrees", "d0")
	D := filepath.Join(T, ".claude", "worktrees", "d0")
	// What git writes: the entry records the worktree's resolved `.git`, and the
	// worktree's `.git` names the entry.
	writeFile(t, filepath.Join(entry, "gitdir"), filepath.Join(D, ".git")+"\n", 0o644)
	writeFile(t, filepath.Join(entry, "commondir"), "../..\n", 0o644)
	writeFile(t, filepath.Join(D, ".git"), "gitdir: "+entry+"\n", 0o644)
	alias := filepath.Join(realTempDir(t), "L")
	symlink(t, base, alias)
	aliasT := filepath.Join(alias, "T")
	aliasD := filepath.Join(aliasT, ".claude", "worktrees", "d0")
	wantResult(t, "remove(L/T,L/D)", remove(t, aliasT, aliasD, ""), `{"success":true}`)
	if exists(D) {
		t.Errorf("remove left the worktree %s", D)
	}
	if exists(entry) {
		t.Errorf("remove left the entry %s", entry)
	}
}

// The hooks refusal reports git's error with a tab turned into a space, as 90fca6e6
// did for C10 (measured on a Linux VM). The fixture makes git fail on an included
// config file whose name holds a tab, which Windows cannot hold. That file has an
// include.path with no value: git ends its listing there, and the daemon's own
// read of the config gives no refusal for it. A syntax error there gets the text
// of the read (TestRepoConfigSyntaxError).
func TestHostileConfigRefusalSpacesOutTabs(t *testing.T) {
	r := newTrustRepo(t)
	bad := filepath.Join(r.base, "x\ty.cfg")
	writeFile(t, bad, "[include]\n\tpath\n", 0o644)
	f, err := os.OpenFile(filepath.Join(r.T, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("[include]\n\tpath = " + filepath.Join(r.base, "x\\ty.cfg") + "\n")
	_ = f.Close()
	rep := info(t, r.T)
	if rep.Error == nil || !strings.Contains(rep.Error.Message, "config-defined hooks could not be pinned off") {
		t.Fatalf("info(T) = %+v %s, want the hooks refusal", rep.Error, rep.Result)
	}
	if strings.Contains(rep.Error.Message, "\t") || !strings.Contains(rep.Error.Message, "x y.cfg") {
		t.Errorf("hooks refusal = %q, want git's tab written as a space", rep.Error.Message)
	}
}

// The operand quoting on a path Windows cannot hold: `"`, newline, tab and DEL are
// escaped, U+00A0 is written  , printable non-ASCII stays as is.
func TestGitDirTrustOperandQuoting(t *testing.T) {
	requireGit(t)
	base := realTempDir(t)
	T := filepath.Join(base, "q\"uo\nte\t\x7f é 𝄞", "T")
	initTrustMain(t, T)
	cd := filepath.Join(T, ".git", "commondir")
	writeFile(t, cd, "x\n", 0o644)
	want := wantTrustPrefix + `"` + base + `/q\"uo\nte\t\x7f é\u00a0𝄞/T/.git/commondir" holds something other than "." or "./"` + wantTRTail
	wantRPCError(t, "info(T)", info(t, T), want)
}

// A `refs` folder of mode 0600 fails the search test of the git-directory test. The
// first git.info of a daemon then answers "no repository" with no settingsControl
// (build 52d82f0a, measured on Linux and macOS). A remove from that repository is
// refused and deletes nothing (claustrum's own, not measured). Mutation: test
// `objects` and `refs` with os.Stat in gitDirPartUsable.
func TestGitDirTrustRefsWithoutSearchPermission(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root passes the search test of every folder")
	}
	r := newTrustRepo(t)
	refs := filepath.Join(r.T, ".git", "refs")
	if err := os.Chmod(refs, 0o600); err != nil {
		t.Fatal(err)
	}
	restore := func() {
		if err := os.Chmod(refs, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = os.Chmod(refs, 0o755) })
	s, _ := newShadowServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "git.info", map[string]any{"path": r.T}))
	if want := `{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"repoSlug":"","defaultBranch":""}}`; got != want {
		t.Errorf("git.info = %s\nwant %s", got, want)
	}
	rep := remove(t, r.T, r.D, "d0")
	restore()
	checkRemoveRefused(t, rep, r.D, "d0", r.T)
	if !exists(filepath.Join(r.T, ".git", "worktrees", "d0")) {
		t.Errorf("remove refused but deleted the entry of %s", r.D)
	}
}

// denyRefsSearch sets `<repo>/.git/refs` to mode 0600 and returns the undo. It
// skips the test as root, which passes the search test of every folder.
func denyRefsSearch(t *testing.T, repo string) func() {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root passes the search test of every folder")
	}
	refs := filepath.Join(repo, ".git", "refs")
	if err := os.Chmod(refs, 0o600); err != nil {
		t.Fatal(err)
	}
	undo := func() {
		if err := os.Chmod(refs, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = os.Chmod(refs, 0o755) })
	return undo
}

// The remove of a gone worktree from a repository whose `refs` has mode 0600 keeps
// the entry, and it keeps the branch with the member (build 52d82f0a, measured on
// Linux and macOS). With an empty branchName the reply has no member. Mutation:
// test `objects` and `refs` with os.Stat in gitDirPartUsable.
func TestGitDirTrustRemoveGoneLeafRefsDenied(t *testing.T) {
	for _, tc := range []struct{ branch, want string }{
		{"d0", `{"success":true,"branchKept":true}`},
		{"", `{"success":true}`},
	} {
		t.Run(tc.branch, func(t *testing.T) {
			r := newTrustRepo(t)
			if err := os.RemoveAll(r.D); err != nil {
				t.Fatal(err)
			}
			undo := denyRefsSearch(t, r.T)
			got := trustRemoveFrame(t, r, tc.branch)
			undo()
			if want := `{"jsonrpc":"2.0","id":1,"result":` + tc.want + `}`; got != want {
				t.Errorf("remove = %s\nwant %s", got, want)
			}
			if !exists(filepath.Join(r.T, ".git", "worktrees", "d0")) || !branchExists(t, r.T, "d0") {
				t.Errorf("remove deleted the entry or the branch d0")
			}
		})
	}
}

// A present worktree in a repository whose `refs` has mode 0600, or whose `refs` is
// gone, gets the lock-check refusal, and nothing is deleted (build 52d82f0a, measured
// on Linux and macOS for mode 0600, on Linux for a gone `refs`). The entry may have no
// commondir, also with GIT_COMMON_DIR=<repo>/.git in the daemon environment (measured
// on Linux and macOS). Mutation: test `objects` and `refs` with os.Stat in
// gitDirPartUsable.
func TestGitDirTrustRemovePresentLeafBrokenRefs(t *testing.T) {
	for _, tc := range []string{"mode 0600", "no commondir", "no commondir, daemon GIT_COMMON_DIR", "refs gone"} {
		t.Run(tc, func(t *testing.T) {
			r := newTrustRepo(t)
			entry := filepath.Join(r.T, ".git", "worktrees", "d0")
			if strings.HasPrefix(tc, "no commondir") {
				if err := os.Remove(filepath.Join(entry, "commondir")); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasSuffix(tc, "GIT_COMMON_DIR") {
				t.Setenv("GIT_COMMON_DIR", filepath.Join(r.T, ".git"))
			}
			refs := filepath.Join(r.T, ".git", "refs")
			var undo func()
			if tc == "refs gone" {
				if err := os.Rename(refs, refs+".off"); err != nil {
					t.Fatal(err)
				}
				undo = func() {
					if err := os.Rename(refs+".off", refs); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				undo = denyRefsSearch(t, r.T)
			}
			got := trustRemoveFrame(t, r, "d0")
			undo()
			want, _ := json.Marshal(worktreeRemoveResult{Success: false, Error: wantLockCheck(r.D)})
			if w := `{"jsonrpc":"2.0","id":1,"result":` + string(want) + `}`; got != w {
				t.Errorf("remove = %s\nwant %s", got, w)
			}
			if !exists(r.D) || !exists(entry) || !branchExists(t, r.T, "d0") {
				t.Errorf("remove refused but deleted the leaf, the entry or the branch")
			}
		})
	}
}

// A create and git.list_branches in a repository whose `refs` has mode 0600 answer
// "no repository", and the create makes nothing (build 52d82f0a, measured on Linux
// and macOS). Mutation: test `objects` and `refs` with os.Stat in gitDirPartUsable.
func TestGitDirTrustCreateAndListRefsDenied(t *testing.T) {
	r := newTrustRepo(t)
	undo := denyRefsSearch(t, r.T)
	s, _ := newSeededShadowServer(t)
	wt := filepath.Join(r.T, ".claude", "worktrees", "w3")
	c := dispatchRaw(t, s, rpcLine(t, "git.worktree_create", map[string]any{
		"baseRepo": r.T, "worktreePath": wt, "branchName": "w3"}))
	l := dispatchRaw(t, s, rpcLine(t, "git.list_branches", map[string]any{"path": r.T}))
	undo()
	if want := `{"jsonrpc":"2.0","id":1,"result":` + notRepoCreate + `}`; c != want {
		t.Errorf("create = %s\nwant %s", c, want)
	}
	if want := `{"jsonrpc":"2.0","id":1,"result":` + notRepoList + `}`; l != want {
		t.Errorf("list_branches = %s\nwant %s", l, want)
	}
	if exists(wt) || branchExists(t, r.T, "w3") {
		t.Errorf("create made the leaf or the branch w3")
	}
}

// An entry with no commondir, in a repository whose `refs` has mode 0600 or whose
// `objects` is a file, keeps the missing-commondir refusal (claustrum's own, not
// measured), as the existence test gave on the earlier code. Mutation: drop the
// missing-commondir test in judgeDamagedRepository.
func TestGitDirTrustEntryWithoutCommondirInBrokenRepository(t *testing.T) {
	for _, tc := range []string{"refs 0600", "objects a file"} {
		t.Run(tc, func(t *testing.T) {
			r := newTrustRepo(t)
			if err := os.Remove(filepath.Join(r.entry(), "commondir")); err != nil {
				t.Fatal(err)
			}
			var undo func()
			if tc == "refs 0600" {
				undo = denyRefsSearch(t, r.T)
			} else {
				obj := filepath.Join(r.T, ".git", "objects")
				if err := os.Rename(obj, obj+".gone"); err != nil {
					t.Fatal(err)
				}
				writeFile(t, obj, "x", 0o644)
				undo = func() {
					if err := os.Remove(obj); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(obj+".gone", obj); err != nil {
						t.Fatal(err)
					}
				}
			}
			got := gitDirTrustFor(r.TW)
			undo()
			if got.verdict != gitDirRefused || got.refusal != wantM3(r.entry()) {
				t.Errorf("trust of TW = %+v, want the refusal %q", got, wantM3(r.entry()))
			}
		})
	}
}
