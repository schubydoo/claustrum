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
// test. The linked worktree's entry is then not an entry, and its commondir counts
// as stray (M1). T itself is then no repository, so remove from T is refused.
func TestGitDirTrustMainHeadSymlinkMakesEntryStray(t *testing.T) {
	r := newTrustRepo(t)
	head := filepath.Join(r.T, ".git", "HEAD")
	if err := os.Rename(head, head+".real"); err != nil {
		t.Fatal(err)
	}
	symlink(t, "HEAD.real", head)
	m1 := wantM1(filepath.Join(r.entry(), "commondir"))
	wantRPCError(t, "info(TW)", info(t, r.TW), m1)
	wantCreateRefused(t, r.TW, r.T, m1)
	wantRemoveRefused(t, r.T, r.D, "d0", r.T)
}

// A `.git` FIFO is skipped and the walk goes on to the outer repository, at
// once.
func TestGitDirTrustDotGitFifoIsSkipped(t *testing.T) {
	r := newTrustRepo(t)
	a := filepath.Join(r.T, "a")
	fifo := filepath.Join(a, ".git")
	mkfifo(t, fifo)
	rep := dispatchWithin(t, fifo, "git.info", map[string]any{"path": a})
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
		writeFile(t, cd, ".\n", 0o644)
		wantRPCError(t, "info(L)", info(t, alias), wantM1(cd))
		wantCreateRefused(t, alias, r.T, wantM1(cd))
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
		writeFile(t, cd, ".\n", 0o644)
		wantRPCError(t, "info(L/deep)", info(t, filepath.Join(alias, "deep")), wantM1(cd))
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

// A remove named through a symlinked root still prunes the entry. Git records the
// worktree's resolved path in the entry. After the fallback delete the worktree is
// gone, so its path is resolved through its nearest existing parent. This is the
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
	// Git's own remove fails, so the fallback delete runs and the prune follows it.
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in\n  *\"worktree remove\"*) echo \"fatal: failed\" >&2; exit 1 ;;\n  *) exit 0 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

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
// config file whose name holds a tab, which Windows cannot hold.
func TestHostileConfigRefusalSpacesOutTabs(t *testing.T) {
	r := newTrustRepo(t)
	bad := filepath.Join(r.base, "x\ty.cfg")
	writeFile(t, bad, "[bad\n", 0o644)
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
	writeFile(t, cd, ".\n", 0o644)
	want := wantTrustPrefix + `"` + base + `/q\"uo\nte\t\x7f é\u00a0𝄞/T/.git/commondir"` + wantM1Tail
	wantRPCError(t, "info(T)", info(t, T), want)
}
