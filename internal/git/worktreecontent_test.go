package git

import (
	"context"
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
		if err == nil && (strings.HasPrefix(d.Name(), shadowReadbackDirPrefix) || strings.HasPrefix(d.Name(), shadowScratchDirPrefix)) {
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
			if flush, _ := envValue(c.env, "GIT_FLUSH"); flush != "1" {
				t.Errorf("GIT_FLUSH of %q = %q, want 1", sub, flush)
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
	// measured on Linux, macOS and Windows).
	if batch, separate := catFileCalls(calls); batch != 1 || separate != 0 {
		t.Errorf("%d cat-file --batch and %d separate cat-file calls, want 1 and 0", batch, separate)
	}
	for _, c := range calls {
		if containsSeq(c.argv, "cat-file", "--batch") && canonicalPath(c.cwd) != canonicalPath(f.repo) {
			t.Errorf("cat-file --batch runs in %s, want the base repository", c.cwd)
		}
	}
}

// TestContentCheckScratchFiles: the long-running hash-object reads each
// object from a file named by its id, in a folder `.readback-scratch-<n>`
// beside `.readback-objects-<n>`. Each file goes right after its answer, so the
// folder holds at most one file, and the folder is gone after the reply
// (4c111e86, measured on Linux and macOS. On Windows the folder is gone after
// the reply).
func TestContentCheckScratchFiles(t *testing.T) {
	f := newContentFixture(t, map[string]string{"CLAUDE.md": "c\n", ".claude/settings.json": "{}\n"})
	type step struct{ op, dir, name string }
	var steps []step
	oldWrite, oldRemove := writeObjectBody, removeObjectBody
	t.Cleanup(func() { writeObjectBody, removeObjectBody = oldWrite, oldRemove })
	writeObjectBody = func(root *os.Root, name string, body []byte) error {
		steps = append(steps, step{"write", root.Name(), name})
		return oldWrite(root, name, body)
	}
	removeObjectBody = func(root *os.Root, name string) error {
		steps = append(steps, step{"remove", root.Name(), name})
		return oldRemove(root, name)
	}
	raw, calls := f.create(t, nil)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	noReadbackFolder(t, f)
	var objects string
	for _, c := range calls {
		if dir, _ := envValue(c.env, "GIT_OBJECT_DIRECTORY"); strings.HasPrefix(filepath.Base(dir), shadowReadbackDirPrefix) {
			objects = dir
		}
	}
	// The commit, the top tree and the tree of .claude.
	if len(steps) != 6 {
		t.Fatalf("steps %v, want a write and a remove for each of 3 objects", steps)
	}
	if steps[0].name != gitRevParse(t, f.repo, "HEAD") {
		t.Errorf("the first file is %q, want the id of the commit", steps[0].name)
	}
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(shadowScratchDirPrefix) + `[0-9]+$`)
	for i, s := range steps {
		want := "write"
		if i%2 == 1 {
			want = "remove"
		}
		if s.op != want || s.dir != steps[0].dir || s.name != steps[i-i%2].name || !isObjectID(s.name) {
			t.Errorf("step %d = %v, want a %s of the file of the object before it in %s", i, s, want, steps[0].dir)
		}
	}
	if !re.MatchString(filepath.Base(steps[0].dir)) || filepath.Dir(steps[0].dir) != filepath.Dir(objects) {
		t.Errorf("the files are in %s, want a folder %s<n> beside %s", steps[0].dir, shadowScratchDirPrefix, objects)
	}
}

// TestContentCheckPartFileGoes: a write of a scratch file that fails after it
// made the file is followed by the remove of that file, and the create
// matches with separate hash-object runs (claustrum's own, not measured).
func TestContentCheckPartFileGoes(t *testing.T) {
	f := newContentFixture(t, map[string]string{".mcp.json": "{}\n"})
	oldWrite, oldRemove := writeObjectBody, removeObjectBody
	t.Cleanup(func() { writeObjectBody, removeObjectBody = oldWrite, oldRemove })
	var failed, removed []string
	writeObjectBody = func(root *os.Root, name string, body []byte) error {
		failed = append(failed, name)
		_ = oldWrite(root, name, body[:len(body)/2])
		return fs.ErrPermission
	}
	removeObjectBody = func(root *os.Root, name string) error {
		removed = append(removed, name)
		return oldRemove(root, name)
	}
	raw, _ := f.create(t, nil)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	if len(failed) == 0 || !slices.Equal(failed, removed) {
		t.Errorf("failed writes %q, removes %q, want a remove of each", failed, removed)
	}
	noReadbackFolder(t, f)
}

// TestRemoveObjectBodyRemovesOnlyAFile: the remove of a scratch file takes
// one regular file by name. A folder or a link at the name stays, and so does
// the target of the link. A name at the path of the home folder is refused
// before any remove (claustrum's own, the guard of each git-shadow delete).
func TestRemoveObjectBodyRemovesOnlyAFile(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeObjectBody(root, "f"); err != nil {
		t.Errorf("remove of a file: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "f")); !os.IsNotExist(err) {
		t.Errorf("the file after its remove: %v, want it gone", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := removeObjectBody(root, "d"); err == nil {
		t.Error("remove of an empty folder: nil, want an error")
	}
	if fi, err := os.Lstat(filepath.Join(dir, "d")); err != nil || !fi.IsDir() {
		t.Errorf("the folder after the remove: %v, want it kept", err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "l")); err != nil {
		t.Logf("no link: %v", err)
	} else {
		if err := removeObjectBody(root, "l"); err == nil {
			t.Error("remove of a link: nil, want an error")
		}
		if _, err := os.Lstat(filepath.Join(dir, "l")); err != nil {
			t.Errorf("the link after the remove: %v, want it kept", err)
		}
		if _, err := os.Stat(target); err != nil {
			t.Errorf("the target of the link: %v, want it kept", err)
		}
	}
	// A regular file at the path of home: only the guard keeps it.
	home := filepath.Join(dir, "h")
	if err := os.WriteFile(home, []byte("h"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := removeObjectBody(root, "h"); err == nil {
		t.Error("remove at the path of home: nil, want the refusal")
	}
	if _, err := os.Lstat(home); err != nil {
		t.Errorf("the file at the path of home after the refusal: %v, want it kept", err)
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
// the header "xyz blob" with no size gives its own line. Separate runs read and hash
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
// once gives the line of a git that stopped answering. Separate runs read and
// hash the commit again and the tree. The create succeeds (c9125eb8, measured
// on Linux), and the checked line counts 2 separate runs.
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

// objectCalls is the object reads and writes of the check in calls, in their
// order: "batch", "buffer", "long <kind>", "read <kind>" and "write <kind>".
// The ids of the separate reads go to ids.
func objectCalls(calls []envCall) (got, ids []string) {
	for _, c := range calls {
		sub := c.argv[slices.Index(c.argv, "core.longpaths=true")+1:]
		switch {
		case slices.Equal(sub, []string{"cat-file", "--batch"}):
			got = append(got, "batch")
		case slices.Equal(sub, []string{"cat-file", "--buffer", "--batch"}):
			got = append(got, "buffer")
		case len(sub) == 3 && sub[0] == "cat-file" && sub[1] != "blob":
			got, ids = append(got, "read "+sub[1]), append(ids, sub[2])
		case len(sub) == 7 && sub[0] == "hash-object" && sub[5] == "--stdin-paths":
			got = append(got, "long "+sub[2])
		case len(sub) == 7 && sub[0] == "hash-object" && sub[5] == "--stdin":
			got = append(got, "write "+sub[2])
		}
	}
	return got, ids
}

// TestContentCheckHashObjectEndsByDepth: after a long-running hash-object that
// exits 1 at once, the level below the top tree is read with one `cat-file
// --buffer --batch` and hashed with a new long-running hash-object of the
// trees. That one exits too, and a separate run reads and hashes the tree
// again. The top tree goes alone, the checked line counts 3 separate runs, and
// one stop line comes. The `cat-file --batch` runs on to the end of the check
// (c9125eb8, measured on Linux).
func TestContentCheckHashObjectEndsByDepth(t *testing.T) {
	f := newContentFixture(t, map[string]string{".mcp.json": "{}\n", ".claude/settings.json": "{}\n"})
	slowGit(t, "hash-object,--stdin-paths,-w", "fail", 0, "", "")
	timeLog := filepath.Join(t.TempDir(), "times")
	t.Setenv("CLAUSTRUM_GITSTUB_TIMELOG", timeLog)
	logs := captureLogBuf(t)
	raw, calls := f.create(t, nil)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	line := "[GitHandler] worktree_create: stopped using the long-running git hash-object for the new worktree's config check: it stopped answering (EOF); the rest is read with a git process per object or per folder depth (slower, same result)"
	if n := strings.Count(logs.String(), line+"\n"); n != 1 {
		t.Errorf("log:\n%s\nwant the line %q once, got %d", logs, line, n)
	}
	if want := checkedLineSep("2", "files", 3, 3, gitRevParse(t, f.repo, "HEAD"), "match"); !want.MatchString(logs.String()) {
		t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
	}
	got, ids := objectCalls(calls)
	want := []string{"batch", "long commit", "read commit", "write commit", "read tree", "write tree",
		"buffer", "long tree", "read tree", "write tree"}
	if !slices.Equal(got, want) {
		t.Fatalf("object calls = %q, want %q", got, want)
	}
	cmd := exec.Command("git", "-C", f.repo, "rev-parse", "HEAD:.claude")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD:.claude: %v", err)
	}
	claude := strings.TrimSpace(string(out))
	if ids[2] != claude {
		t.Errorf("the last separate read is of %s, want the .claude tree %s", ids[2], claude)
	}
	// The batch ends after the start of the last separate read.
	b, err := os.ReadFile(timeLog)
	if err != nil {
		t.Fatal(err)
	}
	batchPid, lastRead, batchEnd := "", -1, -1
	for i, l := range strings.Split(string(b), "\n") {
		w := strings.Split(l, "\x1f")
		switch {
		case w[0] == "start" && slices.Equal(w[len(w)-2:], []string{"cat-file", "--batch"}) && !slices.Contains(w, "--buffer"):
			batchPid = w[2]
		case w[0] == "start" && slices.Equal(w[len(w)-2:], []string{"tree", claude}):
			lastRead = i
		case w[0] == "end" && len(w) > 2 && w[2] == batchPid:
			batchEnd = i
		}
	}
	switch {
	case batchPid == "" || lastRead < 0:
		t.Errorf("the time log has no start of the batch (%q) or of the last separate read (line %d)", batchPid, lastRead)
	case batchEnd < 0:
		t.Errorf("the time log has no end line of the batch %s", batchPid)
	case batchEnd < lastRead:
		t.Errorf("the batch ended at line %d of the time log, the last separate read started at line %d: want the end after it", batchEnd, lastRead)
	}
}

// TestContentCheckBatchOtherObjectByDepth: a `cat-file --batch` that answers
// "xyz blob" stops at the commit. The level below the top tree goes to a
// `cat-file --buffer --batch`, which answers the same, so a separate run reads
// and hashes that tree. No long-running hash-object starts, one stop line
// comes, and the checked line counts 3 separate runs (c9125eb8, measured on
// Linux).
func TestContentCheckBatchOtherObjectByDepth(t *testing.T) {
	f := newContentFixture(t, map[string]string{".mcp.json": "{}\n", ".claude/settings.json": "{}\n"})
	slowGit(t, "cat-file,--batch", "fail", 0, "", "xyz blob\n")
	logs := captureLogBuf(t)
	raw, calls := f.create(t, nil)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	if n := strings.Count(logs.String(), "stopped using the long-running git"); n != 1 {
		t.Errorf("log:\n%s\nwant one stop line, got %d", logs, n)
	}
	if want := checkedLineSep("2", "files", 3, 3, gitRevParse(t, f.repo, "HEAD"), "match"); !want.MatchString(logs.String()) {
		t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
	}
	got, _ := objectCalls(calls)
	want := []string{"batch", "read commit", "write commit", "read tree", "write tree", "buffer", "read tree", "write tree"}
	if !slices.Equal(got, want) {
		t.Errorf("object calls = %q, want %q", got, want)
	}
}

// TestContentCheckBatchOtherObjectOneDepthRead: in a tree of three levels below
// the top, a `cat-file --batch` that answers "xyz blob" stops at the commit.
// The first level goes to one `cat-file --buffer --batch`, which answers the
// same. The other two levels get no such read: separate runs read and hash
// them. One stop line comes, and the checked line counts 5 separate runs
// (c9125eb8, measured on Linux).
func TestContentCheckBatchOtherObjectOneDepthRead(t *testing.T) {
	f := newContentFixture(t, map[string]string{".claude/settings.json": "{}\n", ".claude/a/b/f": "deep\n"})
	slowGit(t, "cat-file,--batch", "fail", 0, "", "xyz blob\n")
	logs := captureLogBuf(t)
	raw, calls := f.create(t, nil)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	if n := strings.Count(logs.String(), "stopped using the long-running git"); n != 1 {
		t.Errorf("log:\n%s\nwant one stop line, got %d", logs, n)
	}
	if want := checkedLineSep("2", "files", 5, 5, gitRevParse(t, f.repo, "HEAD"), "match"); !want.MatchString(logs.String()) {
		t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
	}
	got, _ := objectCalls(calls)
	want := []string{"batch", "read commit", "write commit", "read tree", "write tree",
		"buffer", "read tree", "write tree", "read tree", "write tree", "read tree", "write tree"}
	if !slices.Equal(got, want) {
		t.Errorf("object calls = %q, want %q", got, want)
	}
}

// treeID is the id of the tree at path in the commit HEAD of repo.
func treeID(t *testing.T, repo, path string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "rev-parse", "HEAD:"+path)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD:%s: %v", path, err)
	}
	return strings.TrimSpace(string(out))
}

// twoFolderFixture is a content fixture whose .claude holds two folders, so
// that the level below .claude holds two trees.
func twoFolderFixture(t *testing.T) checkoutRuleFixture {
	t.Helper()
	return newContentFixture(t, map[string]string{".claude/a/x.json": "{}\n", ".claude/b/y.json": "[]\n"})
}

// TestContentCheckLevelReadsByDepth: the first `cat-file --batch` exits 1 at
// once, and every later git works. Each level below the top tree then goes to
// one `cat-file --buffer --batch` and one long-running tree hash-object, with
// no separate read. The second read gets the ids of the level in the order of
// the walk, and the checked line counts all 5 objects. The form follows c9125eb8
// (measured on Linux). The order is claustrum's own. The count of separate
// runs is 5: the commit, the top tree, the read of `.claude` and the two trees
// of the read of a and b. A read per folder depth of one tree counts as one
// run (4c111e86, measured on Linux). A count per tree of a read of two trees
// is claustrum's own.
// Mutation: count no tree of those reads (the count is 2).
func TestContentCheckLevelReadsByDepth(t *testing.T) {
	f := twoFolderFixture(t)
	slowGit(t, "cat-file,--batch", "fail", 0, "", "")
	t.Setenv("CLAUSTRUM_GITSTUB_NTHFILE", filepath.Join(t.TempDir(), "nth"))
	t.Setenv("CLAUSTRUM_GITSTUB_NTH", "1")
	stdin := filepath.Join(t.TempDir(), "stdin")
	t.Setenv("CLAUSTRUM_GITSTUB_STDINLOG", stdin)
	logs := captureLogBuf(t)
	raw, calls := f.create(t, nil)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	if want := checkedLineSep("2", "files", 5, 5, gitRevParse(t, f.repo, "HEAD"), "match"); !want.MatchString(logs.String()) {
		t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
	}
	got, _ := objectCalls(calls)
	want := []string{"batch", "read commit", "write commit", "read tree", "write tree", "buffer", "long tree", "buffer", "long tree"}
	if !slices.Equal(got, want) {
		t.Errorf("object calls = %q, want %q", got, want)
	}
	b, err := os.ReadFile(stdin)
	if err != nil {
		t.Fatal(err)
	}
	wantIn := treeID(t, f.repo, ".claude") + "\n\n" + treeID(t, f.repo, ".claude/a") + "\n" + treeID(t, f.repo, ".claude/b") + "\n"
	if !strings.HasPrefix(string(b), wantIn) {
		t.Errorf("the stdin of the two --buffer reads = %q, want %q first", b, wantIn)
	}
}

// TestContentCheckLevelHasherStopsOnce: every long-running hash-object exits 1
// at once. The tree hash-object of the first level below the top stops, so the
// level of two trees gets no read per folder depth: both trees get a separate
// read and hash. c9125eb8 makes one `cat-file --buffer --batch` in a tree of
// three levels so (measured on Linux). The two trees of one level are
// claustrum's own, not measured.
func TestContentCheckLevelHasherStopsOnce(t *testing.T) {
	f := twoFolderFixture(t)
	slowGit(t, "hash-object,--stdin-paths,-w", "fail", 0, "", "")
	logs := captureLogBuf(t)
	raw, calls := f.create(t, nil)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	if n := strings.Count(logs.String(), "stopped using the long-running git"); n != 1 {
		t.Errorf("log:\n%s\nwant one stop line, got %d", logs, n)
	}
	got, _ := objectCalls(calls)
	want := []string{"batch", "long commit", "read commit", "write commit", "read tree", "write tree",
		"buffer", "long tree", "read tree", "write tree",
		"read tree", "write tree", "read tree", "write tree"}
	if !slices.Equal(got, want) {
		t.Errorf("object calls = %q, want %q", got, want)
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

// TestNoteFileMode: the last core.filemode of a listing counts, `0` and a value
// of `false` give false, a key with no value gives true, a listing with none
// gives true, and a failed listing keeps the value before it (claustrum's own,
// not measured).
func TestNoteFileMode(t *testing.T) {
	c := &contentCheck{}
	for _, row := range []struct {
		name string
		l    listingRun
		off  bool
	}{
		{"false", listingRun{out: []byte("core.filemode\nfalse\x00")}, true},
		{"a failed listing", listingRun{err: errors.New("exit status 1")}, true},
		{"false, then true", listingRun{out: []byte("core.filemode\nfalse\x00core.filemode\ntrue\x00")}, false},
		{"true, then 0", listingRun{out: []byte("core.filemode\ntrue\x00user.name\nx\x00core.filemode\n0\x00")}, true},
		{"no value", listingRun{out: []byte("core.filemode\x00")}, false},
		{"no key", listingRun{out: []byte("core.bare\nfalse\x00")}, false},
	} {
		c.noteFileMode(row.l)
		if c.fileModeOff != row.off {
			t.Errorf("%s: fileModeOff = %v, want %v", row.name, c.fileModeOff, row.off)
		}
	}
}

// TestContentCheckHashOfAnotherID: a separate `hash-object --stdin -w` whose
// answer is not the id of the commit refuses with the repository code. The
// batch fails at once, so the separate runs read and hash (claustrum's own
// text, not measured).
func TestContentCheckHashOfAnotherID(t *testing.T) {
	f := newContentFixture(t, map[string]string{".mcp.json": "{}\n"})
	t.Setenv("CLAUSTRUM_GITSTUB_MATCH2", "cat-file,--batch")
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT2", "1")
	slowGit(t, "hash-object,--stdin,-w", "fail", 0, "", strings.Repeat("e", 40)+"\n")
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "0")
	raw, _ := f.create(t, nil)
	commit := gitRevParse(t, f.repo, "HEAD")
	hint := `Run "git fsck" in ` + fmt.Sprintf("%q", f.repo) + ` to check the repository: git couldn't read what it stores for commit ` +
		commit[:12] + ` (the stored commit does not match its id).`
	if want := contentFrame(t, "worktree_verify_repository", hint); raw != want {
		t.Errorf("git.worktree_create =\n%s\nwant\n%s", raw, want)
	}
}

// TestContentCheckBodyNotWritten: a file of the object that cannot be written
// sends that object to a separate `hash-object --stdin -w`, with no log line,
// and the create matches. The long-running hash-object of each kind starts
// before the write, as in 4c111e86 (measured on Linux), and gets no object (claustrum's own, not
// measured).
func TestContentCheckBodyNotWritten(t *testing.T) {
	f := newContentFixture(t, map[string]string{".mcp.json": "{}\n"})
	old := writeObjectBody
	writeObjectBody = func(*os.Root, string, []byte) error { return fs.ErrPermission }
	t.Cleanup(func() { writeObjectBody = old })
	logs := captureLogBuf(t)
	raw, calls := f.create(t, nil)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	if strings.Contains(logs.String(), "stopped using the long-running git") {
		t.Errorf("log:\n%s\nwant no stop line", logs)
	}
	if want := checkedLine("1", "file", 2, gitRevParse(t, f.repo, "HEAD"), "match"); !want.MatchString(logs.String()) {
		t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
	}
	if long, separate := hashObjectWrites(calls); long != 2 || separate != 2 {
		t.Errorf("%d long-running and %d separate hash-object -w calls, want 2 and 2", long, separate)
	}
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
			if want := checkedLine(row.files, word, row.objects, commit, fmt.Sprintf("refused: %q", loggedHint(hint))); !want.MatchString(logs.String()) {
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

// TestContentCheckPointerTextOtherFilter: a blob in the form of a Git LFS
// pointer whose path has a filter other than lfs keeps the filter call, and a
// smudge with the same output at each run passes (claustrum's own, not
// measured).
func TestContentCheckPointerTextOtherFilter(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ptr := "version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.Repeat("ab", 32) + "\nsize 3\n"
	f := newContentFixture(t, map[string]string{".claude/p.txt": ptr, ".gitattributes": ".claude/p.txt filter=s\n"})
	outside(t, "[filter \"s\"]\n\tsmudge = CLAUSTRUM_TEST_HELPER=filter-suffix '"+filepath.ToSlash(exe)+"' baa\n")
	raw, calls := f.create(t, nil)
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	if !slices.ContainsFunc(calls, func(c envCall) bool { return slices.Contains(c.argv, "--filters") }) {
		t.Error("no cat-file --filters call ran, want one")
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
	if want := checkedLine("0", "files", 2, commit, fmt.Sprintf("refused: %q", loggedHint(hint))); !want.MatchString(logs.String()) {
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
	if want := checkedLine("0", "files", 2, commit, fmt.Sprintf("refused: %q", loggedHint(hint))); !want.MatchString(logs.String()) {
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

// TestContentCheckStopsAtTheDeadline: a `cat-file --batch` that answers nothing
// for 30 s, and the timeoutMs deadline ends while the check waits for it
// (deadlineAt at the gate of the batch). The check stops at once with no
// separate read, logs the checked line with "stopped at the deadline", and the
// create answers the timeout of a good checkout (4c111e86, measured on Linux).
// The copy step then runs its config listing and no `ls-files` call
// (4c111e86, measured on Linux). Mutations: no wait on the deadline (the reply
// comes after 30 s), separate reads after it (a separate cat-file runs), the
// whole copy step after it (its ls-files call runs), or no copy step (one
// listing of baseRepo fewer before the rollback).
func TestContentCheckStopsAtTheDeadline(t *testing.T) {
	f := newContentFixture(t, map[string]string{".mcp.json": "{}\n", ".claude/settings.json": "{}\n"})
	gate := gateStub(t, "cat-file,--batch", 30*time.Second, "", "")
	timeLog, pidFile := filepath.Join(t.TempDir(), "times"), filepath.Join(t.TempDir(), "pid")
	t.Setenv("CLAUSTRUM_GITSTUB_TIMELOG", timeLog)
	t.Cleanup(func() {
		_ = os.WriteFile(gate, nil, 0o644)
		if runtime.GOOS != "windows" {
			waitStubExit(t, timeLog, pidFile, "cat-file\x1f--batch")
		}
	})
	firedDeadlineAt(t, func() bool { return exists(gate + ".in") })
	logs := captureLogBuf(t)
	begin := time.Now()
	raw, calls := f.create(t, map[string]any{"timeoutMs": markerTimeoutMs})
	if took := time.Since(begin); took > 20*time.Second {
		t.Errorf("the create took %v, want an answer at the deadline", took)
	}
	if !strings.Contains(raw, `"errorCode":"timeout"`) || !strings.Contains(raw, "deadline expired after the checkout finished") {
		t.Fatalf("git.worktree_create = %s, want the timeout of a good checkout", raw)
	}
	if want := checkedLineSep("0", "files", 0, 0, gitRevParse(t, f.repo, "HEAD"), "stopped at the deadline"); !want.MatchString(logs.String()) {
		t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
	}
	if strings.Contains(logs.String(), "stopped using the long-running git") {
		t.Errorf("log:\n%s\nwant no stop line of the batch", logs)
	}
	if batch, separate := catFileCalls(calls); batch != 1 || separate != 0 {
		t.Errorf("%d cat-file --batch and %d separate cat-file calls, want 1 and 0", batch, separate)
	}
	// After the batch: the listing of the copy step, then the listing of the
	// branch step of the rollback and its for-each-ref call. An ask about the
	// repository after the 5 s of a kept answer (probeKeep) is left out.
	var after []string
	for i, c := range calls {
		if containsSeq(c.argv, "ls-files", "--others") {
			t.Errorf("the copy step ran after the deadline: %q", c.argv)
		}
		if !containsSeq(c.argv, "cat-file", "--batch") {
			continue
		}
		for _, n := range calls[i+1:] {
			switch {
			case slices.Equal(n.argv, []string{"config", "-z", "--list"}):
				after = append(after, "listing")
			case containsSeq(n.argv, "rev-parse", "--git-dir"):
			default:
				after = append(after, n.argv[len(n.argv)-1])
			}
			if slices.Contains(n.argv, "for-each-ref") {
				break
			}
		}
	}
	if want := []string{"listing", "listing", "refs/heads/"}; !slices.Equal(after, want) {
		t.Errorf("the calls after the batch end with %q, want %q", after, want)
	}
	noReadbackFolder(t, f)
}

// callsAfter names the calls after calls[from] up to the for-each-ref of the
// branch step. An ask about the repository after the 5 s of a kept answer
// (probeKeep) is left out.
func callsAfter(calls []envCall, from int) []string {
	var after []string
	for _, n := range calls[from+1:] {
		switch {
		case slices.Equal(n.argv, []string{"config", "-z", "--list"}):
			after = append(after, "listing")
		case containsSeq(n.argv, "rev-parse", "--git-dir"):
		default:
			after = append(after, n.argv[len(n.argv)-1])
		}
		if slices.Contains(n.argv, "for-each-ref") {
			break
		}
	}
	return after
}

// isFileHash reports the `hash-object --no-filters --stdin-paths` call of the
// files. The long-running hash-object calls end with -w.
func isFileHash(c envCall) bool {
	return slices.Contains(c.argv, "hash-object") && c.argv[len(c.argv)-1] == "--stdin-paths"
}

// TestContentCheckDeadlineDuringAListing: the deadline passes while the config
// listing of the next git call is held. That listing ends, its call does not
// start, and the check stops with the objects hashed so far (4c111e86 and
// 87375cc7, measured on Linux, and 4c111e86 on Windows for the file hash). The
// copy step then runs its listing, though `.claude/` holds only `worktrees`.
// Mutations: no test after the listing of a hash-object (the call starts and
// the count grows), and the old test for a child of `.claude/` (one listing
// fewer).
func TestContentCheckDeadlineDuringAListing(t *testing.T) {
	for _, tc := range []struct {
		name, before string
		objects      int
		notRun       func(envCall) bool
	}{
		{"commit hash", "cat-file,--batch", 0, func(c envCall) bool { return stubMatches(c.argv, "hash-object,commit,--stdin-paths") }},
		{"tree hash", "hash-object,commit,--stdin-paths", 1, func(c envCall) bool { return stubMatches(c.argv, "hash-object,tree,--stdin-paths") }},
		{"file hash", "hash-object,tree,--stdin-paths", 2, isFileHash},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newContentFixture(t, map[string]string{"CLAUDE.md": "c\n", ".mcp.json": "{}\n"})
			if err := os.MkdirAll(filepath.Dir(f.leaf), 0o755); err != nil {
				t.Fatal(err)
			}
			marker := gateStubPre(t, tc.before)
			held := filepath.Join(t.TempDir(), "held")
			t.Setenv("CLAUSTRUM_GITSTUB_MATCH2", "config,-z,--list")
			t.Setenv("CLAUSTRUM_GITSTUB_AFTER2", marker+".in")
			t.Setenv("CLAUSTRUM_GITSTUB_GATE2", held)
			t.Setenv("CLAUSTRUM_GITSTUB_MS2", "30000")
			t.Cleanup(func() { _ = os.WriteFile(held, nil, 0o644) })
			firedDeadlineThen(t, func() bool { return exists(held + ".in") }, func() { _ = os.WriteFile(held, nil, 0o644) })
			logs := captureLogBuf(t)
			raw, calls := f.create(t, map[string]any{"timeoutMs": markerTimeoutMs})
			if !strings.Contains(raw, `"errorCode":"timeout"`) || !strings.Contains(raw, "deadline expired after the checkout finished") {
				t.Fatalf("git.worktree_create = %s, want the timeout of a good checkout", raw)
			}
			commit := gitRevParse(t, f.repo, "HEAD")
			if want := checkedLine("0", "files", tc.objects, commit, "stopped at the deadline"); !want.MatchString(logs.String()) {
				t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
			}
			if slices.ContainsFunc(calls, tc.notRun) {
				t.Errorf("the call after the held listing started: %v", calls)
			}
			if strings.Contains(logs.String(), "stopped using the long-running git") {
				t.Errorf("log:\n%s\nwant no stop line of a long-running git", logs)
			}
			from := slices.IndexFunc(calls, func(c envCall) bool { return stubMatches(c.argv, tc.before) })
			if from < 0 {
				t.Fatalf("no call %q", tc.before)
			}
			// The held listing, the listing of the copy step, and the listing of
			// the branch step with its for-each-ref call.
			if got, want := callsAfter(calls, from), []string{"listing", "listing", "listing", "refs/heads/"}; !slices.Equal(got, want) {
				t.Errorf("the calls after %q are %q, want %q", tc.before, got, want)
			}
			noReadbackFolder(t, f)
		})
	}
}

// TestContentCheckDeadlineBeforeALevelHash: after a stop of the batch, the
// deadline passes during the listing of the tree hash-object of a read per
// folder depth. That git does not start, and the check stops with no stop line
// of that git (claustrum's own, not measured). Mutation: no deadline arm in
// readLevel (the nil git of the level is closed, a panic).
func TestContentCheckDeadlineBeforeALevelHash(t *testing.T) {
	f := twoFolderFixture(t)
	slowGit(t, "cat-file,--batch", "fail", 0, "", "")
	t.Setenv("CLAUSTRUM_GITSTUB_NTHFILE", filepath.Join(t.TempDir(), "nth"))
	t.Setenv("CLAUSTRUM_GITSTUB_NTH", "1")
	stdin := filepath.Join(t.TempDir(), "stdin")
	t.Setenv("CLAUSTRUM_GITSTUB_STDINLOG", stdin)
	held := filepath.Join(t.TempDir(), "held")
	t.Setenv("CLAUSTRUM_GITSTUB_MATCH2", "config,-z,--list")
	t.Setenv("CLAUSTRUM_GITSTUB_AFTER2", stdin)
	t.Setenv("CLAUSTRUM_GITSTUB_GATE2", held)
	t.Setenv("CLAUSTRUM_GITSTUB_MS2", "30000")
	t.Cleanup(func() { _ = os.WriteFile(held, nil, 0o644) })
	firedDeadlineThen(t, func() bool { return exists(held + ".in") }, func() { _ = os.WriteFile(held, nil, 0o644) })
	logs := captureLogBuf(t)
	raw, calls := f.create(t, map[string]any{"timeoutMs": markerTimeoutMs})
	if !strings.Contains(raw, `"errorCode":"timeout"`) {
		t.Fatalf("git.worktree_create = %s, want the timeout of a good checkout", raw)
	}
	if want := checkedLineSep("0", "files", 2, 2, gitRevParse(t, f.repo, "HEAD"), "stopped at the deadline"); !want.MatchString(logs.String()) {
		t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
	}
	if n := strings.Count(logs.String(), "stopped using the long-running git"); n != 1 {
		t.Errorf("log:\n%s\nwant only the stop line of the batch, got %d", logs, n)
	}
	got, _ := objectCalls(calls)
	want := []string{"batch", "read commit", "write commit", "read tree", "write tree", "buffer"}
	if !slices.Equal(got, want) {
		t.Errorf("object calls = %q, want %q", got, want)
	}
}

// gateStubPre makes the first rule of the stub write its gate file with ".in"
// added when a call that match names starts, with no wait, and returns the
// gate file.
func gateStubPre(t *testing.T, match string) string {
	t.Helper()
	gate := filepath.Join(t.TempDir(), "gate")
	slowGit(t, match, "pre", 0, "", "")
	t.Setenv("CLAUSTRUM_GITSTUB_GATE", gate)
	return gate
}

// TestContentCheckDeadlineDuringTheFileHash: the deadline passes during the
// `hash-object --stdin-paths` call of the files. The call gets the kill, its result
// is not used, and the line counts no file (4c111e86 and 87375cc7, measured on
// Linux). A create with no `.claude/` folder runs no listing of the copy step
// (claustrum's own, not measured). Mutations: no test after that call (the line
// ends with match), and no test for the folder (one listing more).
func TestContentCheckDeadlineDuringTheFileHash(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outside bool
		want    []string
	}{
		{"only worktrees in .claude", false, []string{"listing", "listing", "refs/heads/"}},
		{"no .claude folder", true, []string{"listing", "refs/heads/"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newContentFixture(t, map[string]string{"CLAUDE.md": "c\n", ".mcp.json": "{}\n"})
			extra := map[string]any{"timeoutMs": markerTimeoutMs}
			if tc.outside {
				extra["worktreePath"] = filepath.Join(f.repo, "wt", "w1")
				if err := os.RemoveAll(filepath.Join(f.repo, ".claude")); err != nil {
					t.Fatal(err)
				}
			} else if err := os.MkdirAll(filepath.Dir(f.leaf), 0o755); err != nil {
				t.Fatal(err)
			}
			gate := gateStub(t, "hash-object,--stdin-paths", 30*time.Second, "", "")
			contentTestCallOnly(t)
			t.Cleanup(func() { _ = os.WriteFile(gate, nil, 0o644) })
			firedDeadlineThen(t, func() bool { return exists(gate + ".in") }, func() { _ = os.WriteFile(gate, nil, 0o644) })
			logs := captureLogBuf(t)
			raw, calls := f.create(t, extra)
			if !strings.Contains(raw, `"errorCode":"timeout"`) || !strings.Contains(raw, "deadline expired after the checkout finished") {
				t.Fatalf("git.worktree_create = %s, want the timeout of a good checkout", raw)
			}
			if want := checkedLine("0", "files", 2, gitRevParse(t, f.repo, "HEAD"), "stopped at the deadline"); !want.MatchString(logs.String()) {
				t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
			}
			from := slices.IndexFunc(calls, isFileHash)
			if from < 0 {
				t.Fatal("no hash-object call of the files")
			}
			if got := callsAfter(calls, from); !slices.Equal(got, tc.want) {
				t.Errorf("the calls after the file hash are %q, want %q", got, tc.want)
			}
		})
	}
}

// TestContentCheckDeadlineAfterTheCheck: with a timeoutMs that does not expire
// during the check, the line ends with match and the create succeeds (4c111e86
// and 87375cc7, measured on Linux). Mutation: a stop that ignores the deadline
// state fails this test.
func TestContentCheckDeadlineAfterTheCheck(t *testing.T) {
	f := newContentFixture(t, map[string]string{"CLAUDE.md": "c\n", ".mcp.json": "{}\n"})
	logs := captureLogBuf(t)
	raw, _ := f.create(t, map[string]any{"timeoutMs": markerTimeoutMs})
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	if want := checkedLine("2", "files", 2, gitRevParse(t, f.repo, "HEAD"), "match"); !want.MatchString(logs.String()) {
		t.Errorf("log:\n%s\nwant a line that matches %s", logs, want)
	}
}

// waitStubExit waits up to 30 s for the end of the stub call whose argv holds
// match, by its start line in timeLog (CLAUSTRUM_GITSTUB_TIMELOG). So its exit
// lands in no later test. It writes the pid to pidFile for stubExited, and it
// is Unix-only, as stubExited is.
func waitStubExit(t *testing.T, timeLog, pidFile, match string) {
	t.Helper()
	b, _ := os.ReadFile(timeLog)
	pid := ""
	for line := range strings.SplitSeq(string(b), "\n") {
		if f := strings.Split(line, "\x1f"); len(f) > 3 && f[0] == "start" && strings.Contains(line, match) {
			pid = f[2]
		}
	}
	if pid == "" {
		return
	}
	mustWrite(t, pidFile, pid)
	for end := time.Now().Add(30 * time.Second); !stubExited(pidFile); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Errorf("the stub call %s still runs 30 s after its gate opened", pid)
			return
		}
	}
}

// firedDeadlineAt is deadlineAt, and the deadline of the context is the time
// at which it fired. So the time of the LFS keep of the create
// (keepWithinDeadline) counts from that time, as it counts from the time of
// the timer.
func firedDeadlineAt(t *testing.T, ready func() bool) {
	t.Helper()
	firedDeadlineThen(t, ready, nil)
}

// firedDeadlineThen is firedDeadlineAt, and then runs once the deadline fired.
func firedDeadlineThen(t *testing.T, ready func() bool, then func()) {
	t.Helper()
	fired := deadlineAt(t, ready, then)
	marked := callerDeadline
	callerDeadline = func(base context.Context, d time.Duration) (context.Context, context.CancelFunc) {
		ctx, cancel := marked(base, d)
		return firedDeadlineCtx{ctx, fired}, cancel
	}
}

// firedDeadlineCtx is a context of deadlineAt whose Deadline is the time of
// the fire, once it fired.
type firedDeadlineCtx struct {
	context.Context
	fired func() time.Time
}

func (c firedDeadlineCtx) Deadline() (time.Time, bool) {
	if at := c.fired(); !at.IsZero() {
		return at, true
	}
	return c.Context.Deadline()
}
