//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
)

// The git calls of git.status, in the order of 89cb6289. The rows are from Linux
// and macOS VMs, whose call logs are equal line by line. This file is unix only,
// because it compares working directories as text.

// statusCallLog puts the logging git stand-in on PATH, makes root the working
// directory of the daemon, and returns a function that runs one git.status and gives
// its frame and its calls. Each call is one line:
//
//	<cwd>|<A, B or ->|<the argv without the -c pairs>[ +locks]
//
// A is the heavy environment and B the light one, by GIT_ALLOW_PROTOCOL. <fx> is
// root, and <tmp> is the temporary git folder.
func statusCallLog(t *testing.T, root string) func(path, base string) (string, []string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	oldTimeout := gitTimeout
	t.Cleanup(func() { gitTimeout = oldTimeout })
	gitTimeout = 0
	installGitSlowStub(t, realGit)
	empty := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("XDG_CONFIG_HOME", empty)
	t.Setenv("HOME", empty)
	for _, k := range daemonGitKeys {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	resetUserExcludesCache(t)
	resetAttr := func() {
		attrSourceOnce = sync.Once{}
		attrSourceOK = false
	}
	resetAttr()
	t.Cleanup(resetAttr)
	t.Chdir(root)
	ctxlog := filepath.Join(t.TempDir(), "ctx.log")
	t.Setenv("CLAUSTRUM_GITSTUB_CTXLOG", ctxlog)
	tmpDir := regexp.MustCompile(`--git-dir=\S*` + statusGitDirTempPrefix + `\d+`)
	s := newTestServer(t)
	return func(path, base string) (string, []string) {
		t.Helper()
		if err := os.WriteFile(ctxlog, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		raw := dispatchRaw(t, s, rpcLine(t, "git.status", map[string]any{"path": path, "baseRepo": base}))
		if fi, err := os.Stat(ctxlog); err != nil || fi.Size() == 0 {
			return raw, nil
		}
		var lines []string
		for _, c := range readShapeCalls(t, ctxlog) {
			var argv []string
			for i := 0; i < len(c.argv); i++ {
				if c.argv[i] == "-c" {
					i++
					continue
				}
				argv = append(argv, c.argv[i])
			}
			env := "-"
			switch {
			case slices.Contains(c.env, "GIT_ALLOW_PROTOCOL=denied_by_claude_ssh"):
				env = "A"
			case slices.Contains(c.env, "GIT_ALLOW_PROTOCOL=https:ssh"):
				env = "B"
			}
			cwd := c.cwd
			if slices.Contains(c.argv, "core.excludesFile") {
				cwd = "<temp>"
			}
			line := cwd + "|" + env + "|" + strings.Join(argv, " ")
			if slices.Contains(c.env, "GIT_OPTIONAL_LOCKS=0") {
				line += " +locks"
			}
			if slices.Contains(c.env, "GIT_DIR="+os.DevNull) && env != "-" {
				line += " +nodir"
			}
			// A GIT_COMMON_DIR that is not <root>/T/.git shows as " cd=<value>".
			for _, kv := range c.all {
				if v, ok := strings.CutPrefix(kv, "GIT_COMMON_DIR="); ok && v != filepath.Join(root, "T", ".git") {
					line += " cd=" + v
				}
			}
			line = tmpDir.ReplaceAllString(line, "--git-dir=<tmp>")
			lines = append(lines, strings.ReplaceAll(line, root, "<fx>"))
		}
		return raw, lines
	}
}

// statusCommandCalls are the calls of one command of the answer: the three calls
// before it, then the command (K1 calls 7 to 10).
func statusCommandCalls(wt, sent, command string) []string {
	return []string{
		"<fx>|A|--git-dir=<fx>/T/.git config -z --list",
		"<fx>|A|--git-dir=<fx>/T/.git hash-object -t tree /dev/null",
		wt + "|A|--git-dir=<tmp> config -z --list",
		wt + "|A|--attr-source=" + gitEmptyTree + " --git-dir=<tmp> --work-tree=" + sent + " " + command,
	}
}

const (
	callStatus    = "status --porcelain --untracked-files=all --ignore-submodules=all +locks"
	callLsFiles   = "ls-files -s -z +locks"
	callHead      = "rev-parse --verify -q HEAD^{commit}"
	callDiffIndex = "diff-index --cached --raw -z --ignore-submodules=none "
	callEmptyA    = "<fx>|A|--git-dir=<fx>/T/.git config -z --list"
	callEmptyB    = "<fx>|A|--git-dir=<fx>/T/.git hash-object -t tree /dev/null"
)

// statusGateCalls are K1 calls 1 to 5, for a path outside baseRepo.
func statusGateCalls(probeTree string) []string {
	return []string{
		"<temp>|-|config --includes --path core.excludesFile",
		"<fx>/T|A|config -z --list",
		"<fx>/T|A|rev-parse --absolute-git-dir",
		"<fx>/T/.git|B|--git-dir=<fx>/T/.git config -z --list",
		"<fx>/T/.git|B|--git-dir=<fx>/T/.git --work-tree=" + probeTree + " rev-parse --show-toplevel",
	}
}

func wantStatusCalls(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s: %d calls, want %d\ngot:\n  %s\nwant:\n  %s", what, len(got), len(want),
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// Row K1: the 22 calls of a request that answers.
func TestGitStatusCallOrder(t *testing.T) {
	f := newStatusFixture(t)
	run := statusCallLog(t, f.root)
	writeFile(t, filepath.Join(f.W, "t.txt"), "changed\n", 0o644)
	writeFile(t, filepath.Join(f.W, "new.txt"), "n\n", 0o644)
	raw, calls := run(f.W, f.T)
	if want := `{"jsonrpc":"2.0","id":1,"result":{"isRepo":true,"clean":false,"changes":[" M t.txt","?? new.txt"]}}`; raw != want {
		t.Fatalf("K1 frame = %s\nwant %s", raw, want)
	}
	want := append(statusGateCalls("<fx>/W"), "<fx>|-|--attr-source="+gitEmptyTree+" version")
	want = append(want, statusCommandCalls("<fx>/W", "<fx>/W", callStatus)...)
	want = append(want, statusCommandCalls("<fx>/W", "<fx>/W", callLsFiles)...)
	want = append(want, statusCommandCalls("<fx>/W", "<fx>/W", callHead)...)
	want = append(want, statusCommandCalls("<fx>/W", "<fx>/W", callDiffIndex+"HEAD -- +locks")...)
	wantStatusCalls(t, "K1", calls, want)
}

// The rows that stop at the gate, by their calls. The first request of the daemon
// ran before, so the read of the excludes and the --attr-source probe are not in
// these lists.
func TestGitStatusGateCalls(t *testing.T) {
	f := newStatusFixture(t)
	run := statusCallLog(t, f.root)
	if raw, _ := run(f.W, f.T); raw != statusClean {
		t.Fatalf("control = %s", raw)
	}
	gate := statusGateCalls("<fx>/W")[1:]
	plain := filepath.Join(f.root, "P")
	inside := filepath.Join(f.T, ".claude", "worktrees", "s0")
	for _, d := range []string{plain, filepath.Join(plain, ".claude", "worktrees", "r"), inside,
		filepath.Join(f.root, "M", "X")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(f.root, "F"), "x\n", 0o644)
	writeFile(t, filepath.Join(f.root, "M", managedWorktreesMarker), "", 0o644)

	for _, c := range []struct {
		name, path, base string
		want             []string
	}{
		// Rows n10 and n10b1: no git call.
		{"n10", f.W, filepath.Join(plain, ".claude", "worktrees", "r"), nil},
		{"n10b1", f.W, filepath.Join(plain, ".claude", "worktrees"), nil},
		// Row n11c1: no git call.
		{"n11c1", f.W, f.T + "/nosub/..", nil},
		// Row o23: no git call.
		{"o23", f.W, filepath.Join(f.root, "M", "X"), nil},
		// Row n10b2: two calls with GIT_DIR=<null device> in a folder with no repository.
		{"n10b2", f.W, filepath.Join(plain, ".claude"), []string{
			"<fx>/P/.claude|A|config -z --list +nodir",
			"<fx>/P/.claude|A|rev-parse --absolute-git-dir +nodir"}},
		// Rows n09 and n26b: the tests on path come before the fourth call.
		{"n09", filepath.Join(f.root, "F"), f.T, gate[:2]},
		{"n26b", filepath.Join(f.root, "gone"), f.T, gate[:2]},
		// Row KN: a plain folder gets the two calls in the common directory.
		{"KN", plain, f.T, statusGateCalls("<fx>/P")[1:]},
		// Rows A05a and n18b: for a path inside baseRepo, or baseRepo itself, the
		// --work-tree value is baseRepo.
		{"A05a", inside, f.T, statusGateCalls("<fx>/T")[1:]},
		{"n18b", f.T, f.T, statusGateCalls("<fx>/T")[1:]},
		// Row n25d1: an empty path is ".".
		{"n25d1", "", f.T, statusGateCalls(".")[1:]},
	} {
		raw, calls := run(c.path, c.base)
		if raw != statusNotRepo {
			t.Errorf("%s frame = %s\nwant %s", c.name, raw, statusNotRepo)
		}
		wantStatusCalls(t, c.name, calls, c.want)
	}

	// Rows n18 and n18c: no worktrees folder, so two calls.
	t.Run("n18", func(t *testing.T) {
		if err := os.Rename(filepath.Join(f.T, ".git", "worktrees"), filepath.Join(f.T, ".git", "wt.aside")); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_ = os.Rename(filepath.Join(f.T, ".git", "wt.aside"), filepath.Join(f.T, ".git", "worktrees"))
		}()
		raw, calls := run(f.W, f.T)
		if raw != statusNotRepo {
			t.Errorf("frame = %s\nwant %s", raw, statusNotRepo)
		}
		wantStatusCalls(t, "n18", calls, gate[:2])
	})
	// Row v4 (Linux VM): a symlink as the last component of a path inside baseRepo
	// stops before the fourth call.
	t.Run("v4", func(t *testing.T) {
		l := filepath.Join(f.T, ".claude", "worktrees", "lnk")
		mustSymlink(t, f.W, l)
		raw, calls := run(l, f.T)
		if raw != statusNotRepo {
			t.Errorf("frame = %s\nwant %s", raw, statusNotRepo)
		}
		wantStatusCalls(t, "v4", calls, gate[:2])
	})
	// Row y1 (Linux and macOS VMs): W/missing/.. passes the gate cleaned. git then
	// does not start in the path as sent, and no `git version` follows.
	t.Run("y1", func(t *testing.T) {
		sent := f.W + "/missing/.."
		raw, calls := run(sent, f.T)
		want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"config-defined hooks could not be pinned off; git not run: listing the configuration in force: chdir ` + sent + `: no such file or directory"}}`
		if raw != want {
			t.Errorf("frame = %s\nwant %s", raw, want)
		}
		wantStatusCalls(t, "y1", calls, append(slices.Clone(gate), callEmptyA, callEmptyB))
	})
	// Row n15: a bad HEAD stops after the two calls in the common directory.
	t.Run("n15", func(t *testing.T) {
		f.write(t, "HEAD", "garbage\n")
		defer f.write(t, "HEAD", "ref: refs/heads/w\n")
		raw, calls := run(f.W, f.T)
		if raw != statusNotRepo {
			t.Errorf("frame = %s\nwant %s", raw, statusNotRepo)
		}
		wantStatusCalls(t, "n15", calls, gate)
	})
	// Row n13: the config.worktree listing is the sixth call, in the daemon's cwd.
	cfg := "<fx>|-|config --no-includes --file - --list -z"
	t.Run("n13", func(t *testing.T) {
		f.write(t, "config.worktree", "[user]\n\tname = x\n")
		raw, calls := run(f.W, f.T)
		if raw != statusNotRepo {
			t.Errorf("frame = %s\nwant %s", raw, statusNotRepo)
		}
		wantStatusCalls(t, "n13", calls, append(slices.Clone(gate), cfg))
	})
	// Row n12: a config.worktree that passes adds that one call.
	t.Run("n12", func(t *testing.T) {
		f.write(t, "config.worktree", "[core]\n\tsparseCheckout = true\n")
		defer func() { _ = os.Remove(filepath.Join(f.entry, "config.worktree")) }()
		raw, calls := run(f.W, f.T)
		if raw != statusClean {
			t.Errorf("frame = %s\nwant %s", raw, statusClean)
		}
		if len(calls) != 21 || calls[4] != cfg {
			t.Errorf("n12: %d calls, call 5 = %q\nwant 21 calls with %q fifth", len(calls), calls[min(4, len(calls)-1)], cfg)
		}
	})
	// Rows n16, o13 and o14d: HEAD names no commit. The two calls of the empty tree
	// run once more, and diff-index gets that id.
	t.Run("n16", func(t *testing.T) {
		f.write(t, "HEAD", "ref: refs/heads/nosuch\n")
		defer f.write(t, "HEAD", "ref: refs/heads/w\n")
		raw, calls := run(f.W, f.T)
		if want := `{"jsonrpc":"2.0","id":1,"result":{"isRepo":true,"clean":false,"changes":["A  .gitignore","A  t.txt"]}}`; raw != want {
			t.Errorf("frame = %s\nwant %s", raw, want)
		}
		want := slices.Clone(gate)
		want = append(want, statusCommandCalls("<fx>/W", "<fx>/W", callStatus)...)
		want = append(want, statusCommandCalls("<fx>/W", "<fx>/W", callLsFiles)...)
		want = append(want, statusCommandCalls("<fx>/W", "<fx>/W", callHead)...)
		want = append(want, callEmptyA, callEmptyB)
		want = append(want, statusCommandCalls("<fx>/W", "<fx>/W", callDiffIndex+gitEmptyTree+" -- +locks")...)
		wantStatusCalls(t, "n16", calls, want)
	})
	// Row n06: the --work-tree of the fifth call is the resolved path. The status
	// calls get the path as sent.
	t.Run("n06", func(t *testing.T) {
		l := filepath.Join(f.root, "L")
		mustSymlink(t, f.W, l)
		raw, calls := run(l, f.T)
		if raw != statusClean {
			t.Fatalf("frame = %s\nwant %s", raw, statusClean)
		}
		if calls[3] != gate[3] {
			t.Errorf("n06 call 4 = %q\nwant %q", calls[3], gate[3])
		}
		if want := statusCommandCalls("<fx>/W", "<fx>/L", callStatus)[3]; calls[7] != want {
			t.Errorf("n06 call 8 = %q\nwant %q", calls[7], want)
		}
	})
	// Row n25c: a relative path passes the gate, git gets it as sent, and status
	// exits 128.
	t.Run("n25c", func(t *testing.T) {
		raw, calls := run("W", f.T)
		if want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"exit status 128"}}`; raw != want {
			t.Errorf("frame = %s\nwant %s", raw, want)
		}
		want := append(statusGateCalls("W")[1:], statusCommandCalls("<fx>/W", "W", callStatus)...)
		wantStatusCalls(t, "n25c", calls, want)
	})
}

// Rows n17 and n17b to n17f, and row o22: one failing git call each.
func TestGitStatusFailingCalls(t *testing.T) {
	f := newStatusFixture(t)
	run := statusCallLog(t, f.root)
	if raw, _ := run(f.W, f.T); raw != statusClean {
		t.Fatalf("control = %s", raw)
	}
	exit1 := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"exit status 1"}}`
	for _, c := range []struct {
		name, match, want string
		calls             int
	}{
		{"n17b absolute-git-dir", "rev-parse,--absolute-git-dir", statusNotRepo, 2},
		{"n17 show-toplevel", "rev-parse,--show-toplevel", statusNotRepo, 4},
		{"n17c status", "status,--porcelain", exit1, 8},
		{"n17d ls-files", "ls-files,-s", exit1, 12},
		{"n17e diff-index", "diff-index,--cached", exit1, 20},
		{"n17f hash-object", "hash-object", statusClean, 20},
		// Row o22: the listing with --git-dir=<common> fails. `git version` follows.
		{"o22 listing in the common directory", "--git-dir=" + filepath.Join(f.T, ".git") + ",--list",
			`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"config-defined hooks could not be pinned off; git not run: listing the configuration in force: exit status 1: boom"}}`, 4},
	} {
		t.Run(c.name, func(t *testing.T) {
			stderr := ""
			if strings.HasPrefix(c.name, "o22") {
				stderr = "boom"
			}
			slowGit(t, c.match, "fail", 0, stderr, "")
			raw, calls := run(f.W, f.T)
			if raw != c.want {
				t.Errorf("frame = %s\nwant %s", raw, c.want)
			}
			if len(calls) != c.calls {
				t.Errorf("%d calls, want %d:\n  %s", len(calls), c.calls, strings.Join(calls, "\n  "))
			}
		})
	}
}

// Rows o20 and o21: the read of the user's excludes runs before a trust refusal, so
// a refused request makes that one call.
func TestGitStatusExcludesReadBeforeTrustRefusal(t *testing.T) {
	f := newStatusFixture(t)
	run := statusCallLog(t, f.root)
	writeFile(t, filepath.Join(f.T, ".git", "commondir"), "x\n", 0o644)
	raw, calls := run(f.W, f.T)
	if !strings.Contains(raw, `"code":-32603`) || !strings.Contains(raw, "commondir not as git writes it") {
		t.Errorf("frame = %s\nwant the stray commondir refusal", raw)
	}
	wantStatusCalls(t, "o21", calls, statusGateCalls("")[:1])
}

// Rows E07a and E07b (Linux and macOS VMs): the daemon's own environment sets
// GIT_COMMON_DIR. The calls with --git-dir=<common> keep the daemon's value. The
// calls in the temporary folder get GIT_COMMON_DIR=<T>/.git.
func TestGitStatusDaemonCommonDirCalls(t *testing.T) {
	f := newStatusFixture(t)
	x := filepath.Join(f.root, "X")
	runGit(t, f.root, "init", "-q", "-b", "main", x)
	runGit(t, x, "commit", "-q", "--allow-empty", "-m", "c0")
	run := statusCallLog(t, f.root)
	t.Setenv("GIT_COMMON_DIR", filepath.Join(x, ".git"))
	raw, calls := run(f.W, f.T)
	if raw != statusClean {
		t.Fatalf("frame = %s\nwant %s", raw, statusClean)
	}
	if len(calls) != 22 {
		t.Fatalf("%d calls, want 22:\n  %s", len(calls), strings.Join(calls, "\n  "))
	}
	daemon := " cd=<fx>/X/.git"
	for i, c := range calls {
		n := i + 1
		// Calls 9, 10, 13, 14, 17, 18, 21 and 22 run in the temporary folder.
		inTemp := n >= 9 && (n-9)%4 < 2
		if strings.HasSuffix(c, daemon) == inTemp {
			t.Errorf("call %d = %q\nwant the daemon's GIT_COMMON_DIR: %v", n, c, !inTemp)
		}
	}
}
