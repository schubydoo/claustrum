//go:build unix

package git

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestContentCheckLinkToAFifo: a committed link goes on no hash-object list,
// so a link to a FIFO does not block the check. The log line counts the
// hashed files only, and the blob of the link (measured on Linux).
func TestContentCheckLinkToAFifo(t *testing.T) {
	f := newContentFixture(t, map[string]string{
		"CLAUDE.md": "c\n", "AGENTS.md": "a\n", ".mcp.json": "{}\n", ".claude/settings.json": "{}\n",
	})
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.repo, ".claude", "x")
	if err := os.Symlink(fifo, link); err != nil {
		t.Fatal(err)
	}
	runGit(t, f.repo, "add", "-f", "--", ".claude/x")
	runGit(t, f.repo, "commit", "-q", "-m", "link")
	stdin := filepath.Join(t.TempDir(), "stdin.log")
	t.Setenv("CLAUSTRUM_GITSTUB_STDINLOG", stdin)
	logs := captureLogBuf(t)
	start := time.Now()
	raw, _ := f.create(t, nil)
	if took := time.Since(start); took > 30*time.Second {
		t.Errorf("the create took %s, want no wait for the FIFO", took)
	}
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	b, err := os.ReadFile(stdin)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Fields(string(b)); len(lines) != 4 || strings.Contains(string(b), filepath.Join(".claude", "x")) {
		t.Errorf("the stdin of hash-object --stdin-paths = %q, want the 4 files and no link", lines)
	}
	if want := checkedLine("4", "files", 4, gitRevParse(t, f.repo, "HEAD"), "match"); !want.MatchString(logs.String()) {
		t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
	}
}

// TestContentCheckUndoThatCannotFinish: a file of the check that cannot be
// read refuses with the retry code. When the rollback cannot remove an entry
// of the leaf, the frame says so after "Removing it could not finish for",
// and the registration and the branch stay (measured on Windows, with a held
// file. Here a folder of mode 0555 stands in for it).
func TestContentCheckUndoThatCannotFinish(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root removes entries of a folder of mode 0555")
	}
	f := newContentFixture(t, map[string]string{"CLAUDE.md": "c\n", ".claude/settings.json": "{}\n"})
	dir := filepath.Join(f.leaf, ".claude")
	t.Cleanup(func() {
		_ = os.Chmod(dir, 0o755)
		_ = os.Chmod(filepath.Join(dir, "settings.json"), 0o644)
	})
	afterCheckout(t, []string{"chmod", filepath.Join(dir, "settings.json"), "000"}, []string{"chmod", dir, "555"})
	raw, calls := f.create(t, nil)
	if slices.ContainsFunc(calls, func(c envCall) bool { return c.argv[len(c.argv)-1] == "--stdin-paths" }) {
		t.Error("a hash-object --stdin-paths call ran, want the refusal before it (measured on Windows)")
	}
	start := `"error":"Claude could not confirm that the new worktree matches its commit, so it did not keep it. Try again, and if this repeats check the permissions of the folder the worktree is made in: \".claude/settings.json\" in the new worktree couldn't be read`
	undo := `. Removing it could not finish for ` + strings.Trim(jsonQuoted(t, f.leaf), `"`) + `: the worktree directory, its registration, and the branch all remain; remove them by hand before retrying (RemoveAll .claude: `
	if !strings.Contains(raw, start) || !strings.Contains(raw, undo) || !strings.HasSuffix(raw, `).","errorCode":"worktree_verify_retry"}}`) {
		t.Errorf("git.worktree_create =\n%s\nwant the retry text, then %q, and a final period", raw, undo)
	}
	if !exists(f.reg) || !branchExists(t, f.repo, "w1") {
		t.Errorf("registration stays %v, branch stays %v, want both", exists(f.reg), branchExists(t, f.repo, "w1"))
	}
}

// commitExecutable makes CLAUDE.md mode 100755 in the commit of f.
func commitExecutable(t *testing.T, f checkoutRuleFixture) {
	t.Helper()
	if err := os.Chmod(filepath.Join(f.repo, "CLAUDE.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, f.repo, "add", "--", "CLAUDE.md")
	runGit(t, f.repo, "commit", "-q", "-m", "executable")
}

// TestContentCheckLostExecBit: a file that the commit records with mode
// 100755 and that has no execute bit right after the checkout refuses the
// create with the content code, before the hash-object call, and the rollback
// removes the worktree (b3081b51, measured on Linux and macOS). A chmod at that
// call passes, because the mode is read first (measured on Linux), and so does
// a file that stays executable (measured). With core.fileMode false in the
// repository the file passes (measured on Linux and macOS). No config call of
// its own reads the setting (c9125eb8, measured on Linux, macOS and Windows).
// The value comes from the config listing, whose last core.filemode counts,
// and `off` is false too (claustrum's own, not measured).
func TestContentCheckLostExecBit(t *testing.T) {
	chmodAfter := func(t *testing.T, p string) { afterCheckout(t, []string{"chmod", p, "644"}) }
	for _, row := range []struct {
		name   string
		setup  func(t *testing.T, p string)
		ok     bool
		config [][]string
	}{
		{"0644 after the checkout", func(t *testing.T, p string) {
			afterCheckout(t, []string{"chmod", p, "644"})
		}, false, nil},
		{"0644 at the hash call", func(t *testing.T, p string) {
			slowGit(t, "hash-object,--stdin-paths", "pre", 0, "", "")
			contentTestCallOnly(t)
			t.Setenv("CLAUSTRUM_GITSTUB_ACTION", "steps")
			t.Setenv("CLAUSTRUM_GITSTUB_VALUE", "chmod\x1f"+p+"\x1f644")
		}, true, nil},
		{"0755 stays", func(*testing.T, string) {}, true, nil},
		{"core.fileMode false", chmodAfter, true, [][]string{{"core.fileMode", "false"}}},
		{"core.fileMode off", chmodAfter, true, [][]string{{"core.fileMode", "off"}}},
		{"core.fileMode false, then true", chmodAfter, false,
			[][]string{{"core.fileMode", "false"}, {"--add", "core.fileMode", "true"}}},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newContentFixture(t, map[string]string{"CLAUDE.md": "c\n"})
			commitExecutable(t, f)
			for _, c := range row.config {
				runGit(t, f.repo, append([]string{"config"}, c...)...)
			}
			row.setup(t, filepath.Join(f.leaf, "CLAUDE.md"))
			logs := captureLogBuf(t)
			raw, calls := f.create(t, nil)
			noFileModeCall(t, calls)
			if row.ok {
				if !strings.Contains(raw, `"success":true`) {
					t.Fatalf("git.worktree_create = %s, want success", raw)
				}
				return
			}
			commit := gitRevParse(t, f.repo, "HEAD")
			hint := `Run "git config core.fileMode false" in ` + fmt.Sprintf("%q", f.repo) +
				` if this file system doesn't keep executable bits, otherwise "git fsck" there: "CLAUDE.md" came out not executable though commit ` +
				commit[:12] + ` records it executable.`
			if want := contentFrame(t, "worktree_verify_content", hint); raw != want {
				t.Errorf("git.worktree_create =\n%s\nwant\n%s", raw, want)
			}
			if want := checkedLine("0", "files", 2, commit, fmt.Sprintf("refused: %q", strings.TrimSuffix(hint, "."))); !want.MatchString(logs.String()) {
				t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
			}
			f.wantGone(t)
		})
	}
}

// noFileModeCall tests that no config call of core.fileMode ran: c9125eb8
// makes none (measured on Linux, macOS and Windows).
func noFileModeCall(t *testing.T, calls []envCall) {
	t.Helper()
	for _, c := range calls {
		if containsSeq(c.argv, "--bool", "--get-regexp") {
			t.Errorf("a config call %q, want none", c.argv)
		}
	}
}

// TestContentCheckLinkChains: a committed link CLAUDE.md whose chain runs in a
// circle, or through /proc or /dev/fd, refuses the create with the link code,
// and the rollback removes the worktree (b3081b51, measured on Linux and
// macOS). Links to README and to /etc/hosts pass (measured). A chain of 8
// links in the worktree that goes on through /bin/sh, /etc/hosts or 3 links
// outside it passes, and a chain of 9 is refused (measured on Linux and macOS
// for the system links, on macOS for the count).
func TestContentCheckLinkChains(t *testing.T) {
	const tooLong = `Shorten the chain of links that starts at "CLAUDE.md" in this repository, or remove the link that leads back: it runs through more than 8 links, or in a circle, further than this check follows (commit `
	const throughProc = `Point the link "CLAUDE.md", or the link it leads through, somewhere that does not pass through /proc or /dev/fd (directly or by a link): what a path there names depends on the program reading it, so what Claude Code would read through it cannot be checked (commit `
	// chain gives the links CLAUDE.md -> l1 -> ... -> l<n-1> -> end, n links.
	// The text {out} in end names a folder outside the repository with the
	// links o1 -> o2 -> o3 -> file and the circle c1 -> c2 -> c3 -> c1.
	chain := func(n int, end string) map[string]string {
		links := map[string]string{"CLAUDE.md": "l1"}
		for i := 1; i < n; i++ {
			links[fmt.Sprintf("l%d", i)] = fmt.Sprintf("l%d", i+1)
		}
		links[fmt.Sprintf("l%d", n-1)] = end
		return links
	}
	rows := []struct {
		name  string
		links map[string]string
		hint  string
	}{
		{"a link to itself", map[string]string{"CLAUDE.md": "CLAUDE.md"}, tooLong},
		{"a circle of 3", map[string]string{"CLAUDE.md": "l1", "l1": "l2", "l2": "CLAUDE.md"}, tooLong},
		{"a chain of 9", chain(9, "README"), tooLong},
		{"a chain of 8", chain(8, "README"), ""},
		{"a chain of 8 and 3 outside", chain(8, "{out}/o1"), ""},
		{"a circle of 3 outside", map[string]string{"CLAUDE.md": "{out}/c1"}, ""},
		{"a chain of 8 and /bin/sh", chain(8, "/bin/sh"), ""},
		{"a chain of 8 and /etc/hosts", chain(8, "/etc/hosts"), ""},
		{"a chain of 9 and /etc/hosts", chain(9, "/etc/hosts"), tooLong},
		{"/dev/fd/0", map[string]string{"CLAUDE.md": "/dev/fd/0"}, throughProc},
		{"/dev/fd/../null", map[string]string{"CLAUDE.md": "/dev/fd/../null"}, throughProc},
		{"/dev/stdin", map[string]string{"CLAUDE.md": "/dev/stdin"}, throughProc},
		{"README", map[string]string{"CLAUDE.md": "README"}, ""},
		{"/etc/hosts", map[string]string{"CLAUDE.md": "/etc/hosts"}, ""},
	}
	if runtime.GOOS == "linux" {
		rows = append(rows, struct {
			name  string
			links map[string]string
			hint  string
		}{"/proc/self/cwd/README", map[string]string{"CLAUDE.md": "/proc/self/cwd/README"}, throughProc})
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			for _, need := range []string{"/etc/hosts", "/bin/sh"} {
				if _, err := os.Stat(need); strings.HasSuffix(row.name, need) && err != nil {
					t.Skipf("no %s: %v", need, err)
				}
			}
			f := newContentFixture(t, map[string]string{"README": "r\n"})
			out := t.TempDir()
			writeFile(t, filepath.Join(out, "file"), "x\n", 0o644)
			for name, target := range map[string]string{"o1": "o2", "o2": "o3", "o3": "file", "c1": "c2", "c2": "c3", "c3": "c1"} {
				if err := os.Symlink(target, filepath.Join(out, name)); err != nil {
					t.Fatal(err)
				}
			}
			names := []string{}
			for name, target := range row.links {
				target = strings.ReplaceAll(target, "{out}", out)
				if err := os.Symlink(target, filepath.Join(f.repo, name)); err != nil {
					t.Fatal(err)
				}
				names = append(names, name)
			}
			runGit(t, f.repo, append([]string{"add", "--"}, names...)...)
			runGit(t, f.repo, "commit", "-q", "-m", "links")
			raw, _ := f.create(t, nil)
			if row.hint == "" {
				if !strings.Contains(raw, `"success":true`) {
					t.Fatalf("git.worktree_create = %s, want success", raw)
				}
				return
			}
			hint := row.hint + gitRevParse(t, f.repo, "HEAD")[:12] + `).`
			if want := contentFrame(t, "worktree_verify_link", hint); raw != want {
				t.Errorf("git.worktree_create =\n%s\nwant\n%s", raw, want)
			}
			f.wantGone(t)
		})
	}
}

// TestContentCheckHashesComparedBlobs: a name with a line end under .claude
// takes the call of the files away, so every file goes to the compare step.
// There each `cat-file blob <id>` is followed by `hash-object -t blob
// --literally --no-filters --stdin -w` into the folder of the check (c9125eb8,
// measured on Linux and macOS). An NTFS folder cannot hold the name, so Windows
// is not measured.
func TestContentCheckHashesComparedBlobs(t *testing.T) {
	f := newContentFixture(t, map[string]string{
		".claude/settings.json": "{}\n", ".claude/we\nird.md": "w\n", ".mcp.json": "{}\n", "CLAUDE.md": "c\n",
	})
	raw, calls := f.create(t, nil)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	var reads, hashes []int
	for i, c := range calls {
		switch {
		case containsSeq(c.argv, "cat-file", "blob"):
			reads = append(reads, i)
		case containsSeq(c.argv, "-t", "blob") && containsSeq(c.argv, "--stdin", "-w"):
			hashes = append(hashes, i)
			if dir, _ := envValue(c.env, "GIT_OBJECT_DIRECTORY"); !strings.HasPrefix(filepath.Base(dir), shadowReadbackDirPrefix) {
				t.Errorf("GIT_OBJECT_DIRECTORY of the blob hash = %q, want a folder %s<n>", dir, shadowReadbackDirPrefix)
			}
		case c.argv[len(c.argv)-1] == "--stdin-paths":
			t.Errorf("a call of the files ran: %q, want none", c.argv)
		}
	}
	if len(reads) != 4 || len(hashes) != 4 {
		t.Fatalf("%d cat-file blob and %d blob hash calls, want 4 and 4", len(reads), len(hashes))
	}
	for i := range reads {
		if hashes[i] <= reads[i] || (i+1 < len(reads) && hashes[i] >= reads[i+1]) {
			t.Errorf("blob hash %d is call %d, want it right after the read of call %d", i+1, hashes[i], reads[i])
		}
	}
}
