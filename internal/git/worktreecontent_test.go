package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// gitRevParse is the full id that ref names in repo.
func gitRevParse(t *testing.T, repo, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "rev-parse", "--verify", ref+"^{commit}")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse %s: %v", ref, err)
	}
	return strings.TrimSpace(string(out))
}

// newContentFixture is a seeded shadow fixture whose commit has files, by
// path with "/" and text.
func newContentFixture(t *testing.T, files map[string]string) checkoutRuleFixture {
	t.Helper()
	f := newCheckoutRuleFixture(t)
	names := make([]string, 0, len(files))
	for name, text := range files {
		writeFile(t, filepath.Join(f.repo, filepath.FromSlash(name)), text, 0o644)
		names = append(names, name)
	}
	runGit(t, f.repo, append([]string{"add", "-f", "--"}, names...)...)
	runGit(t, f.repo, "commit", "-q", "-m", "content")
	return f
}

// afterCheckout makes the read-tree call of the next create run steps right
// after git ended, in the form of the "steps" action of the git stub.
func afterCheckout(t *testing.T, steps ...[]string) {
	t.Helper()
	joined := make([]string, len(steps))
	for i, s := range steps {
		joined[i] = strings.Join(s, "\x1f")
	}
	slowGit(t, "read-tree", "post", 0, "", "")
	t.Setenv("CLAUSTRUM_GITSTUB_ACTION", "steps")
	t.Setenv("CLAUSTRUM_GITSTUB_VALUE", strings.Join(joined, "\x1e"))
}

// contentFrame is the reply of a create that the content check refused.
func contentFrame(t *testing.T, code, hint string) string {
	t.Helper()
	return `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` +
		jsonQuoted(t, contentRefusalStart+hint) + `,"errorCode":"` + code + `"}}`
}

// checkedLine matches the log line of one check.
func checkedLine(files, word string, objects int, commit, end string) *regexp.Regexp {
	return regexp.MustCompile(regexp.QuoteMeta("[GitHandler] worktree_create: checked "+files+" "+word+
		" under .claude, .mcp.json, CLAUDE.md (or CLAUDE.local.md) and AGENTS.md against "+fmt.Sprint(objects)+
		" objects of commit "+commit[:12]+" in ") + `[0-9.a-zµ]+` + regexp.QuoteMeta(": "+end))
}

func noReadbackFolder(t *testing.T, f checkoutRuleFixture) {
	t.Helper()
	root := filepath.Join(f.run, shadowRootName)
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), shadowReadbackDirPrefix) {
			t.Errorf("%s stays after the create", p)
		}
		return nil
	})
}

// TestContentCheckMatch: a create whose files match the commit succeeds and
// logs the checked line (measured on Linux, macOS and Windows). The calls run
// in the base repository, and the writes of hash-object go to a folder
// `.readback-objects-<n>` of the folder of the daemon that is gone after the
// reply (measured on Linux).
func TestContentCheckMatch(t *testing.T) {
	f := newContentFixture(t, map[string]string{
		"CLAUDE.md": "c\n", "AGENTS.md": "a\n", ".mcp.json": "{}\n", ".claude/settings.json": "{}\n", "other.txt": "o\n",
	})
	logs := captureLogBuf(t)
	raw, calls := f.create(t, nil)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	commit := gitRevParse(t, f.repo, "HEAD")
	if want := checkedLine("4", "files", 3, commit, "match"); !want.MatchString(logs.String()) {
		t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
	}
	noReadbackFolder(t, f)
	var sawWrite, sawPaths bool
	for _, c := range calls {
		sub := c.argv[slices.Index(c.argv, "core.longpaths=true")+1:]
		switch {
		case slices.Equal(sub, []string{"hash-object", "-t", "commit", "--literally", "--no-filters", "--stdin", "-w"}):
			sawWrite = true
			dir, _ := envValue(c.env, "GIT_OBJECT_DIRECTORY")
			if !strings.HasPrefix(filepath.Base(dir), shadowReadbackDirPrefix) {
				t.Errorf("GIT_OBJECT_DIRECTORY of %q = %q, want a folder %s<n>", sub, dir, shadowReadbackDirPrefix)
			}
		case slices.Equal(sub, []string{"hash-object", "--no-filters", "--stdin-paths"}):
			sawPaths = true
		default:
			continue
		}
		if canonicalPath(c.cwd) != canonicalPath(f.repo) {
			t.Errorf("%q runs in %s, want the base repository", sub, c.cwd)
		}
	}
	if !sawWrite || !sawPaths {
		t.Errorf("hash-object -w ran %v, --stdin-paths ran %v, want both", sawWrite, sawPaths)
	}
}

// TestContentCheckRefusals: a change of the new worktree right after the
// checkout refuses the create with its code and text, and the rollback removes
// the leaf, the registration and the branch (measured on Linux, macOS and
// Windows).
func TestContentCheckRefusals(t *testing.T) {
	for _, row := range []struct {
		name  string
		unix  bool
		steps func(leaf string) [][]string
		code  string
		hint  func(repo, commit string) string
		// objects is the count in the log line: the commit, the tree, and
		// the blob of a file whose id differs. files is the count of files.
		objects int
		files   string
	}{
		{"a changed file", false, func(leaf string) [][]string {
			return [][]string{{"write", filepath.Join(leaf, "CLAUDE.md"), "c\nmore\n"}}
		}, "worktree_verify_content", func(repo, commit string) string {
			return `Check whether a filter that rewrites "CLAUDE.md" on checkout gives different output each time it runs: that file in the new worktree didn't match commit ` +
				commit + `. Less often, two committed names that differ only in letter case, or a damaged repository (run "git fsck" in ` + fmt.Sprintf("%q", repo) + ` to check), cause this.`
		}, 3, "1"},
		{"a new AGENTS.md", false, func(leaf string) [][]string {
			return [][]string{{"write", filepath.Join(leaf, "AGENTS.md"), "a\n"}}
		}, "worktree_verify_other_writer", func(repo, commit string) string {
			return `Check what else writes into the folder the worktree is made in, or run "git fsck" in ` + fmt.Sprintf("%q", repo) +
				`: "AGENTS.md" appeared in the new worktree though commit ` + commit + ` has no such file.`
		}, 2, "1"},
		{"a file that cannot be read", true, func(leaf string) [][]string {
			return [][]string{{"chmod", filepath.Join(leaf, "CLAUDE.md"), "000"}}
		}, "worktree_verify_retry", func(repo, commit string) string {
			return `Try again, and if this repeats check the permissions of the folder the worktree is made in: "CLAUDE.md" in the new worktree couldn't be read to check it against commit ` +
				commit + ` (` + syscall.EACCES.Error() + `).`
		}, 2, "0"},
	} {
		t.Run(row.name, func(t *testing.T) {
			if row.unix {
				if runtime.GOOS == "windows" {
					t.Skip("a mode of 000 is a Unix test")
				}
				if os.Getuid() == 0 {
					t.Skip("root reads a file of mode 000")
				}
			}
			f := newContentFixture(t, map[string]string{"CLAUDE.md": "c\n"})
			afterCheckout(t, row.steps(f.leaf)...)
			logs := captureLogBuf(t)
			raw, _ := f.create(t, nil)
			commit := gitRevParse(t, f.repo, "HEAD")
			hint := row.hint(f.repo, commit[:12])
			if want := contentFrame(t, row.code, hint); raw != want {
				t.Errorf("git.worktree_create =\n%s\nwant\n%s", raw, want)
			}
			word := "file"
			if row.files != "1" {
				word = "files"
			}
			// The log quotes the text with no final period (measured on Linux).
			if want := checkedLine(row.files, word, row.objects, commit, fmt.Sprintf("refused: %q", strings.TrimSuffix(hint, "."))); !want.MatchString(logs.String()) {
				t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
			}
			f.wantGone(t)
			noReadbackFolder(t, f)
		})
	}
}

// TestContentCheckLinks: a committed link CLAUDE.md that leads to the top of
// the worktree refuses the create (measured on Linux, macOS and Windows). A
// link to a folder outside the repository passes (measured).
func TestContentCheckLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a committed link needs core.symlinks")
	}
	for _, row := range []struct {
		target string
		code   string
	}{{".", "worktree_verify_link"}, {"../outside", ""}} {
		t.Run(row.target, func(t *testing.T) {
			f := newCheckoutRuleFixture(t)
			if err := os.Symlink(row.target, filepath.Join(f.repo, "CLAUDE.md")); err != nil {
				t.Fatal(err)
			}
			runGit(t, f.repo, "add", "--", "CLAUDE.md")
			runGit(t, f.repo, "commit", "-q", "-m", "link")
			raw, _ := f.create(t, nil)
			if row.code == "" {
				if !strings.Contains(raw, `"success":true`) {
					t.Fatalf("git.worktree_create = %s, want success", raw)
				}
				return
			}
			commit := gitRevParse(t, f.repo, "HEAD")[:12]
			hint := `Point the link "CLAUDE.md", or the link it leads through, at a folder inside the repository instead of its top: everything in the repository could be read through it unchecked (commit ` + commit + `).`
			if want := contentFrame(t, row.code, hint); raw != want {
				t.Errorf("git.worktree_create =\n%s\nwant\n%s", raw, want)
			}
			f.wantGone(t)
		})
	}
}

// TestContentCheckStoppedCall: a `cat-file commit` call of the check that its
// limit stops gives the repository code with the stop text, and the rollback
// runs (measured on Linux, macOS and Windows). The log line counts no file and
// no object (measured on Linux).
func TestContentCheckStoppedCall(t *testing.T) {
	f := newContentFixture(t, map[string]string{"CLAUDE.md": "c\n"})
	shortGitLimit(t, 500*time.Millisecond)
	setDuration(t, &gitStopGrace, 200*time.Millisecond)
	slowGit(t, "cat-file,commit", "fail", 60*time.Second, "", "")
	logs := captureLogBuf(t)
	start := time.Now()
	raw, _ := f.create(t, nil)
	if took := time.Since(start); took > 30*time.Second {
		t.Errorf("the create took %s, want an end after the limit", took)
	}
	commit := gitRevParse(t, f.repo, "HEAD")
	hint := `Run "git fsck" in ` + fmt.Sprintf("%q", f.repo) + ` to check the repository: git couldn't read what it stores for commit ` +
		commit[:12] + ` (` + stopText("cat-file") + `).`
	if want := contentFrame(t, "worktree_verify_repository", hint); raw != want {
		t.Errorf("git.worktree_create =\n%s\nwant\n%s", raw, want)
	}
	if want := checkedLine("0", "files", 0, commit, "refused: "); !want.MatchString(logs.String()) {
		t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
	}
	f.wantGone(t)
}

// TestContentCheckStoppedHashGoesOn: a stopped `hash-object --stdin-paths`
// call stops no other call. The files are compared with their blobs, and the
// create succeeds (measured on macOS).
func TestContentCheckStoppedHashGoesOn(t *testing.T) {
	f := newContentFixture(t, map[string]string{"CLAUDE.md": "c\n"})
	shortGitLimit(t, 500*time.Millisecond)
	setDuration(t, &gitStopGrace, 200*time.Millisecond)
	slowGit(t, "hash-object,--stdin-paths", "fail", 60*time.Second, "", "")
	logs := captureLogBuf(t)
	raw, calls := f.create(t, nil)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	if !slices.ContainsFunc(calls, func(c envCall) bool {
		return containsSeq(c.argv, "cat-file", "blob")
	}) {
		t.Error("no cat-file blob call, want the compare step")
	}
	if want := checkedLine("1", "file", 3, gitRevParse(t, f.repo, "HEAD"), "match"); !want.MatchString(logs.String()) {
		t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
	}
}

// containsSeq reports whether argv holds a and then b right after it.
func containsSeq(argv []string, a, b string) bool {
	i := slices.Index(argv, a)
	return i >= 0 && i+1 < len(argv) && argv[i+1] == b
}

// TestContentCheckFilters: a smudge filter of the user config whose output is
// the same at each run passes, and one whose output changes refuses with the
// content code (measured on macOS and Windows).
func TestContentCheckFilters(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		suffix string
		ok     bool
	}{{"baa", true}, {"time", false}} {
		t.Run(row.suffix, func(t *testing.T) {
			f := newContentFixture(t, map[string]string{"CLAUDE.md": "c\n", ".gitattributes": "CLAUDE.md filter=s\n"})
			outside(t, "[filter \"s\"]\n\tsmudge = CLAUSTRUM_TEST_HELPER=filter-suffix '"+filepath.ToSlash(exe)+"' "+row.suffix+"\n")
			raw, _ := f.create(t, nil)
			if row.ok {
				if !strings.Contains(raw, `"success":true`) {
					t.Fatalf("git.worktree_create = %s, want success", raw)
				}
				return
			}
			if !strings.Contains(raw, `"errorCode":"worktree_verify_content"`) {
				t.Fatalf("git.worktree_create = %s, want worktree_verify_content", raw)
			}
			f.wantGone(t)
		})
	}
}

// TestContentCheckNotWithTheOffSwitch: a create with "shadowDirOff":true runs
// no check (claustrum's own, not measured).
func TestContentCheckNotWithTheOffSwitch(t *testing.T) {
	f := newContentFixture(t, map[string]string{"CLAUDE.md": "c\n"})
	afterCheckout(t, []string{"write", filepath.Join(f.leaf, "CLAUDE.md"), "changed\n"})
	// sourceBranch gives the add a commit id, so the read-tree call names an id
	// in this state too.
	raw, calls := f.create(t, map[string]any{"shadowDirOff": true, "sourceBranch": "main"})
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	if slices.ContainsFunc(calls, func(c envCall) bool { return slices.Contains(c.argv, "cat-file") }) {
		t.Error("a cat-file call ran, want no check")
	}
}

// TestContentCheckReadHeldElsewherePasses: a read that fails with the error of
// readHeldElsewhere passes the compare, and no git call runs. On Windows that
// error is the sharing violation (the reference answers success, measured on
// Windows). Here the error of a file of mode 000 stands in for it.
func TestContentCheckReadHeldElsewherePasses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("TestContentCheckSharingViolationPasses holds the file on Windows")
	}
	if os.Getuid() == 0 {
		t.Skip("root reads a file of mode 000")
	}
	leaf := t.TempDir()
	writeFile(t, filepath.Join(leaf, "CLAUDE.md"), "c\n", 0o000)
	f := contentFile{path: "CLAUDE.md", mode: "100644", id: strings.Repeat("0", 40)}
	c := &contentCheck{leaf: leaf, commit: strings.Repeat("0", 40)}
	if r := c.compare(f); r == nil || r.ErrorCode != "worktree_verify_retry" {
		t.Fatalf("compare with no held error = %+v, want worktree_verify_retry", r)
	}
	setReadHeldElsewhere(t, syscall.EACCES)
	if r := c.compare(f); r != nil {
		t.Errorf("compare = %+v, want a pass", r)
	}
}

// setReadHeldElsewhere sets readHeldElsewhere to err for the test.
func setReadHeldElsewhere(t *testing.T, err error) {
	t.Helper()
	old := readHeldElsewhere
	readHeldElsewhere = err
	t.Cleanup(func() { readHeldElsewhere = old })
}

// TestContentCheckChangedDuringTheHash: a racer replaces CLAUDE.md by a link
// to "." right before the `hash-object --stdin-paths` call. The create answers
// the other_writer code with "it changed while it was being checked", logs no
// file, and runs no cat-file blob call (measured on Linux).
func TestContentCheckChangedDuringTheHash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the racer makes a symbolic link")
	}
	f := newContentFixture(t, map[string]string{"CLAUDE.md": "c\n"})
	p := filepath.Join(f.leaf, "CLAUDE.md")
	slowGit(t, "hash-object,--stdin-paths", "pre", 0, "", "")
	t.Setenv("CLAUSTRUM_GITSTUB_ACTION", "steps")
	t.Setenv("CLAUSTRUM_GITSTUB_VALUE", "rm\x1f"+p+"\x1esymlink\x1f.\x1f"+p)
	logs := captureLogBuf(t)
	raw, calls := f.create(t, nil)
	commit := gitRevParse(t, f.repo, "HEAD")
	hint := `Check what else writes into the folder the worktree is made in, or run "git fsck" in ` + fmt.Sprintf("%q", f.repo) +
		`: "CLAUDE.md" in the new worktree isn't what commit ` + commit[:12] + ` has there (it changed while it was being checked).`
	if want := contentFrame(t, "worktree_verify_other_writer", hint); raw != want {
		t.Errorf("git.worktree_create =\n%s\nwant\n%s", raw, want)
	}
	if want := checkedLine("0", "files", 2, commit, fmt.Sprintf("refused: %q", strings.TrimSuffix(hint, "."))); !want.MatchString(logs.String()) {
		t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
	}
	if slices.ContainsFunc(calls, func(c envCall) bool { return containsSeq(c.argv, "cat-file", "blob") }) {
		t.Error("a cat-file blob call ran, want none")
	}
	f.wantGone(t)
}
