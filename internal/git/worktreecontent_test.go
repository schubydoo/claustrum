package git

import (
	"errors"
	"fmt"
	"io/fs"
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

// checkedLine matches the log line of one check whose commit and trees came
// through the batch.
func checkedLine(files, word string, objects int, commit, end string) *regexp.Regexp {
	return checkedLineSep(files, word, objects, 0, commit, end)
}

// checkedLineSep is checkedLine with sep commits and trees read by separate
// runs.
func checkedLineSep(files, word string, objects, sep int, commit, end string) *regexp.Regexp {
	return regexp.MustCompile(regexp.QuoteMeta("[GitHandler] worktree_create: checked "+files+" "+word+
		" under .claude, .mcp.json, CLAUDE.md (or CLAUDE.local.md) and AGENTS.md against "+fmt.Sprint(objects)+
		" objects of commit "+commit[:12]+" ("+fmt.Sprint(sep)+" of the commits and folder listings read by separate git runs) in ") +
		`[0-9.a-zµ]+` + regexp.QuoteMeta(": "+end))
}

// noSHA1 makes the checks of the test run as in a process that cannot
// compute SHA-1: with no batch.
func noSHA1(t *testing.T) {
	t.Helper()
	old := sha1Works
	sha1Works = func() bool { return false }
	t.Cleanup(func() { sha1Works = old })
}

// contentTestCallOnly narrows the first rule of the git stub to the
// `hash-object --no-filters --stdin-paths` call of the files: its last argument
// is --stdin-paths, where the long-running hash-object calls end with -w.
func contentTestCallOnly(t *testing.T) {
	t.Helper()
	t.Setenv("CLAUSTRUM_GITSTUB_ARGLAST", "--stdin-paths")
}

// catFileCalls counts the `cat-file --batch` calls and the separate
// `cat-file commit` and `cat-file tree` calls of calls.
func catFileCalls(calls []envCall) (batch, separate int) {
	for _, c := range calls {
		switch {
		case containsSeq(c.argv, "cat-file", "--batch"):
			batch++
		case containsSeq(c.argv, "cat-file", "commit"), containsSeq(c.argv, "cat-file", "tree"):
			separate++
		}
	}
	return batch, separate
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
// reply, with no alternates (measured on Linux). One long-running hash-object
// of each kind hashes the commit and the trees (c9125eb8, measured on Linux,
// macOS and Windows).
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
	writes := map[string]int{}
	var sawPaths bool
	for _, c := range calls {
		sub := c.argv[slices.Index(c.argv, "core.longpaths=true")+1:]
		switch {
		case len(sub) == 7 && sub[0] == "hash-object" && slices.Equal(sub[3:], []string{"--literally", "--no-filters", "--stdin-paths", "-w"}):
			writes[sub[2]]++
			dir, _ := envValue(c.env, "GIT_OBJECT_DIRECTORY")
			if !strings.HasPrefix(filepath.Base(dir), shadowReadbackDirPrefix) {
				t.Errorf("GIT_OBJECT_DIRECTORY of %q = %q, want a folder %s<n>", sub, dir, shadowReadbackDirPrefix)
			}
			if alt, ok := envValue(c.env, "GIT_ALTERNATE_OBJECT_DIRECTORIES"); !ok || alt != "" {
				t.Errorf("GIT_ALTERNATE_OBJECT_DIRECTORIES of %q = %q, %v, want set and empty", sub, alt, ok)
			}
		case containsSeq(sub, "--stdin", "-w"):
			t.Errorf("a separate %q call, want none", sub)
		case slices.Equal(sub, []string{"hash-object", "--no-filters", "--stdin-paths"}):
			sawPaths = true
		default:
			continue
		}
		if canonicalPath(c.cwd) != canonicalPath(f.repo) {
			t.Errorf("%q runs in %s, want the base repository", sub, c.cwd)
		}
	}
	if writes["commit"] != 1 || writes["tree"] != 1 || len(writes) != 2 || !sawPaths {
		t.Errorf("long-running hash-object calls %v, --stdin-paths ran %v, want one of commit and one of tree, and the call of the files", writes, sawPaths)
	}
	// One `cat-file --batch` reads the commit and the trees (c9125eb8,
	// measured on Linux and macOS).
	if batch, separate := catFileCalls(calls); batch != 1 || separate != 0 {
		t.Errorf("%d cat-file --batch and %d separate cat-file calls, want 1 and 0", batch, separate)
	}
	for _, c := range calls {
		if containsSeq(c.argv, "cat-file", "--batch") && canonicalPath(c.cwd) != canonicalPath(f.repo) {
			t.Errorf("cat-file --batch runs in %s, want the base repository", c.cwd)
		}
	}
}

// TestContentCheckBatchEnds: a `cat-file --batch` that exits 1 at once gives
// the line of a batch that stopped answering. Separate runs read the commit and
// the tree. The create succeeds, and the checked line counts 2 separate runs
// (c9125eb8, measured on Linux).
func TestContentCheckBatchEnds(t *testing.T) {
	f := newContentFixture(t, map[string]string{".mcp.json": "{}\n"})
	slowGit(t, "cat-file,--batch", "fail", 0, "", "")
	logs := captureLogBuf(t)
	raw, calls := f.create(t, nil)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	stopped := "[GitHandler] worktree_create: stopped using the long-running git cat-file for the new worktree's config check: it stopped answering (EOF); the rest is read with a git process per object or per folder depth (slower, same result)"
	if !strings.Contains(logs.String(), stopped+"\n") {
		t.Errorf("log:\n%s\nwant the line %q", logs, stopped)
	}
	if want := checkedLineSep("1", "file", 2, 2, gitRevParse(t, f.repo, "HEAD"), "match"); !want.MatchString(logs.String()) {
		t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
	}
	if batch, separate := catFileCalls(calls); batch != 1 || separate != 2 {
		t.Errorf("%d cat-file --batch and %d separate cat-file calls, want 1 and 2", batch, separate)
	}
}

// TestContentCheckBatchOtherObject: a `cat-file --batch` that answers with
// the header of another object gives its own line. Separate runs read and hash
// the commit and the tree, and no long-running hash-object starts (c9125eb8,
// measured on Linux).
func TestContentCheckBatchOtherObject(t *testing.T) {
	f := newContentFixture(t, map[string]string{".mcp.json": "{}\n"})
	slowGit(t, "cat-file,--batch", "fail", 0, "", "xyz blob\n")
	logs := captureLogBuf(t)
	raw, calls := f.create(t, nil)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	line := "[GitHandler] worktree_create: stopped using the long-running git cat-file for the new worktree's config check: it answered with something other than the object asked for; the rest is read with a git process per object or per folder depth (slower, same result)"
	if !strings.Contains(logs.String(), line+"\n") {
		t.Errorf("log:\n%s\nwant the line %q", logs, line)
	}
	if want := checkedLineSep("1", "file", 2, 2, gitRevParse(t, f.repo, "HEAD"), "match"); !want.MatchString(logs.String()) {
		t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
	}
	if long, separate := hashObjectWrites(calls); long != 0 || separate != 2 {
		t.Errorf("%d long-running and %d separate hash-object -w calls, want 0 and 2", long, separate)
	}
}

// TestContentCheckHashObjectEnds: a long-running hash-object that exits 1 at
// once gives the line of a git that stopped answering. The batch ends too, and
// separate runs read and hash the commit again and the tree. The create
// succeeds, and the checked line counts 2 separate runs (c9125eb8, measured
// on Linux).
func TestContentCheckHashObjectEnds(t *testing.T) {
	f := newContentFixture(t, map[string]string{".mcp.json": "{}\n"})
	slowGit(t, "hash-object,--stdin-paths,-w", "fail", 0, "", "")
	logs := captureLogBuf(t)
	raw, calls := f.create(t, nil)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	line := "[GitHandler] worktree_create: stopped using the long-running git hash-object for the new worktree's config check: it stopped answering (EOF); the rest is read with a git process per object or per folder depth (slower, same result)"
	if n := strings.Count(logs.String(), line+"\n"); n != 1 {
		t.Errorf("log:\n%s\nwant the line %q once, got %d", logs, line, n)
	}
	if want := checkedLineSep("1", "file", 2, 2, gitRevParse(t, f.repo, "HEAD"), "match"); !want.MatchString(logs.String()) {
		t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
	}
	if batch, separate := catFileCalls(calls); batch != 1 || separate != 2 {
		t.Errorf("%d cat-file --batch and %d separate cat-file calls, want 1 and 2", batch, separate)
	}
	if long, separate := hashObjectWrites(calls); long != 1 || separate != 2 {
		t.Errorf("%d long-running and %d separate hash-object -w calls, want 1 and 2", long, separate)
	}
}

// hashObjectWrites counts the long-running `hash-object ... --stdin-paths -w`
// calls and the separate `hash-object ... --stdin -w` calls of calls.
func hashObjectWrites(calls []envCall) (long, separate int) {
	for _, c := range calls {
		switch {
		case containsSeq(c.argv, "--stdin-paths", "-w"):
			long++
		case containsSeq(c.argv, "--stdin", "-w"):
			separate++
		}
	}
	return long, separate
}

// TestContentCheckWaitsForTheBatch: the create answers only after the
// `cat-file --batch` git read the end of its stdin and exited. The stub writes
// its record 1 s after the real git ended (claustrum's own, not measured).
func TestContentCheckWaitsForTheBatch(t *testing.T) {
	f := newContentFixture(t, map[string]string{".mcp.json": "{}\n"})
	record := filepath.Join(t.TempDir(), "batch-ended")
	slowGit(t, "cat-file,--batch", "post", 0, "", "")
	t.Setenv("CLAUSTRUM_GITSTUB_ACTION", "steps")
	t.Setenv("CLAUSTRUM_GITSTUB_VALUE", "sleep\x1f1000\x1ewrite\x1f"+record+"\x1fended")
	raw, _ := f.create(t, nil)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	if _, err := os.Stat(record); err != nil {
		t.Errorf("the batch git had not ended at the reply: %v", err)
	}
}

// TestContentCheckNoSHA1: a process that cannot compute SHA-1 logs its line,
// starts no `cat-file --batch`, and reads the commit and the tree by separate
// runs. The checked line counts 2 (c9125eb8, measured on Linux with
// GODEBUG=fips140=only).
func TestContentCheckNoSHA1(t *testing.T) {
	f := newContentFixture(t, map[string]string{".mcp.json": "{}\n"})
	noSHA1(t)
	logs := captureLogBuf(t)
	raw, calls := f.create(t, nil)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	line := "[GitHandler] worktree_create: this process cannot compute SHA-1 (GODEBUG=fips140=only), so the new worktree's config check reads each commit, folder and link with separate git processes instead of the long-running ones (slower, same result)"
	if !strings.Contains(logs.String(), line+"\n") {
		t.Errorf("log:\n%s\nwant the line %q", logs, line)
	}
	if want := checkedLineSep("1", "file", 2, 2, gitRevParse(t, f.repo, "HEAD"), "match"); !want.MatchString(logs.String()) {
		t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
	}
	if batch, separate := catFileCalls(calls); batch != 0 || separate != 2 {
		t.Errorf("%d cat-file --batch and %d separate cat-file calls, want 0 and 2", batch, separate)
	}
}

// TestSHA1WorksUnderFIPSOnly: with GODEBUG=fips140=only the probe reports
// false, and with no GODEBUG it reports true.
func TestSHA1WorksUnderFIPSOnly(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		godebug, want string
	}{{"fips140=only", "false"}, {"", "true"}} {
		cmd := exec.Command(exe)
		cmd.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "sha1-works", "GODEBUG": row.godebug})
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("GODEBUG=%s: %v", row.godebug, err)
		}
		if got := strings.TrimSpace(string(out)); got != row.want {
			t.Errorf("GODEBUG=%s: sha1Works = %s, want %s", row.godebug, got, row.want)
		}
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
		}, 2, "0"},
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
			logs := captureLogBuf(t)
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
			// The blob of the link is read before the test, and it counts
			// (measured on Windows).
			if want := checkedLine("0", "files", 3, gitRevParse(t, f.repo, "HEAD"), "refused: "); !want.MatchString(logs.String()) {
				t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
			}
			f.wantGone(t)
		})
	}
}

// TestContentCheckStoppedCall: a separate `cat-file commit` call of the check that its
// limit stops gives the repository code with the stop text, and the rollback
// runs (measured on Linux, macOS and Windows). The log line counts no file and
// no object (measured on Linux).
func TestContentCheckStoppedCall(t *testing.T) {
	f := newContentFixture(t, map[string]string{"CLAUDE.md": "c\n"})
	// With no batch the commit is read by a separate run.
	noSHA1(t)
	// The limit is that of every call of the create. 5 s is far over a fast
	// call on a loaded runner and far under the hold of the stub.
	shortGitLimit(t, 5*time.Second)
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
	// The limit is that of every call of the create. At 500 ms a loaded
	// Windows runner stopped the rev-parse call before the add. 5 s is far
	// over a fast call and far under the hold of the stub.
	shortGitLimit(t, 5*time.Second)
	setDuration(t, &gitStopGrace, 200*time.Millisecond)
	slowGit(t, "hash-object,--stdin-paths", "fail", 60*time.Second, "", "")
	contentTestCallOnly(t)
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

// TestContentCheckCompareRetry: the compare of a file that cannot be read
// gives the retry code with the error of the system, and no git call runs.
func TestContentCheckCompareRetry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a mode of 000 is a Unix test")
	}
	if os.Getuid() == 0 {
		t.Skip("root reads a file of mode 000")
	}
	leaf := t.TempDir()
	writeFile(t, filepath.Join(leaf, "CLAUDE.md"), "c\n", 0o000)
	f := contentFile{path: "CLAUDE.md", mode: "100644", id: strings.Repeat("0", 40)}
	c := &contentCheck{leaf: leaf, commit: strings.Repeat("0", 40)}
	if r := c.compare(f); r == nil || r.ErrorCode != "worktree_verify_retry" || !strings.HasSuffix(r.hint, "("+syscall.EACCES.Error()+").") {
		t.Fatalf("compare = %+v, want worktree_verify_retry with %q", r, syscall.EACCES.Error())
	}
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
	contentTestCallOnly(t)
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

// TestContentCheckFileGoneDuringTheHash: a racer removes AGENTS.md right
// before the `hash-object --stdin-paths` call. The create answers the retry
// code with the error of the system, "no such file or directory" on Linux,
// and the log counts no file (measured on Linux).
func TestContentCheckFileGoneDuringTheHash(t *testing.T) {
	f := newContentFixture(t, map[string]string{"CLAUDE.md": "c\n", "AGENTS.md": "a\n"})
	p := filepath.Join(f.leaf, "AGENTS.md")
	_, gone := os.Lstat(filepath.Join(t.TempDir(), "missing"))
	var pe *fs.PathError
	if !errors.As(gone, &pe) {
		t.Fatalf("Lstat of a missing file = %v", gone)
	}
	slowGit(t, "hash-object,--stdin-paths", "pre", 0, "", "")
	contentTestCallOnly(t)
	t.Setenv("CLAUSTRUM_GITSTUB_ACTION", "steps")
	t.Setenv("CLAUSTRUM_GITSTUB_VALUE", "rm\x1f"+p)
	logs := captureLogBuf(t)
	raw, _ := f.create(t, nil)
	commit := gitRevParse(t, f.repo, "HEAD")
	hint := `Try again, and if this repeats check the permissions of the folder the worktree is made in: "AGENTS.md" in the new worktree couldn't be read to check it against commit ` +
		commit[:12] + ` (` + pe.Err.Error() + `).`
	if want := contentFrame(t, "worktree_verify_retry", hint); raw != want {
		t.Errorf("git.worktree_create =\n%s\nwant\n%s", raw, want)
	}
	if want := checkedLine("0", "files", 2, commit, fmt.Sprintf("refused: %q", strings.TrimSuffix(hint, "."))); !want.MatchString(logs.String()) {
		t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
	}
	f.wantGone(t)
}

// TestContentUndoText: after a refusal of the check, a failed rollback text
// starts " Removing it could not finish for " and ends with a period
// (measured on Windows). An empty text stays empty.
func TestContentUndoText(t *testing.T) {
	in := "; and the undo could not finish for C:\\R\\w1: the worktree directory, its registration, and the branch all remain; remove them by hand before retrying (RemoveAll CLAUDE.md: x)"
	want := " Removing it could not finish for C:\\R\\w1: the worktree directory, its registration, and the branch all remain; remove them by hand before retrying (RemoveAll CLAUDE.md: x)."
	if got := contentUndoText(in); got != want {
		t.Errorf("contentUndoText = %q\nwant %q", got, want)
	}
	if got := contentUndoText(""); got != "" {
		t.Errorf("contentUndoText of no text = %q, want none", got)
	}
}
