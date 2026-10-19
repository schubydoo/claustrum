package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The tests of the points that the validation round val-s3p2b2-3 found. Each
// names its row.

// pinnedInfo is the Lstat of path with its file identity read now. On Windows
// os.SameFile reads the identity of a FileInfo from the path at the first
// compare. Without this read, a compare after a replace sees the new file two
// times and calls it the old file (val-s3p2b2-3-windows part C).
func pinnedInfo(t *testing.T, path string) os.FileInfo {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	os.SameFile(fi, fi)
	return fi
}

// TestShadowConfigCarriesSha1AndNoopV1: the copy carries
// `extensions.objectformat = sha1` and `extensions.noop-v1 = true`, with format
// version 0 and 1 and in both letter cases of the key. The texts are those of
// 5fd08069 in cells X8 of val-s3p2b2-3-linux2 (Linux VM): 192 and 187 bytes.
func TestShadowConfigCarriesSha1AndNoopV1(t *testing.T) {
	requireGit(t)
	home := resolveTestRoot(t, t.TempDir())
	const blocks = "[core]\n\tfilemode = \"true\"\n[core]\n\tbare = \"false\"\n[core]\n\tlogallrefupdates = \"true\"\n"
	for _, row := range []struct {
		name, key, value, block string
		size                    int
	}{
		{"X8-objectFormat-sha1", "objectFormat", "sha1", "[extensions]\n\tobjectformat = \"sha1\"\n", 192},
		{"X8-OBJECTFORMAT-sha1", "OBJECTFORMAT", "sha1", "[extensions]\n\tobjectformat = \"sha1\"\n", 192},
		{"X8-noop-v1-true", "noop-v1", "true", "[extensions]\n\tnoop-v1 = \"true\"\n", 187},
		{"X8-NOOP-V1-true", "NOOP-V1", "true", "[extensions]\n\tnoop-v1 = \"true\"\n", 187},
	} {
		for _, version := range []string{"0", "1"} {
			config := "[core]\n\trepositoryformatversion = " + version + "\n" + baseRepoConfig +
				"[extensions]\n\t" + row.key + " = " + row.value + "\n"
			got, err := copyTextOf(t, map[string]string{"config": config}, home)
			if err != nil {
				t.Fatal(err)
			}
			want := "[core]\n\trepositoryformatversion = \"" + version + "\"\n" + blocks + row.block + shadowConfigTail
			if len(want) != row.size {
				t.Fatalf("%s: the wanted text has %d bytes, the cell has %d", row.name, len(want), row.size)
			}
			if got != want {
				t.Errorf("%s, version %s:\n got %q\nwant %q", row.name, version, got, want)
			}
		}
	}
}

// TestWriteNewFile: a copy is written as a new file through a file
// `.tmp-<digits>` of mode 0600 in the folder of the copy, and no such file is
// left (val-s3p2b2-3-macos row D7, macOS VM, and val-s3p2b2-3-linux2 row D5,
// Linux VM).
func TestWriteNewFile(t *testing.T) {
	dir := resolveTestRoot(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(dir, "info"), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	for _, name := range []string{"config", filepath.Join("info", "exclude")} {
		if left, err := writeNewFile(root, name, []byte("one\n")); err != nil || left != "" {
			t.Fatalf("%s: %q, %v", name, left, err)
		}
		first := pinnedInfo(t, filepath.Join(dir, name))
		if left, err := writeNewFile(root, name, []byte("two\n")); err != nil || left != "" {
			t.Fatalf("%s again: %q, %v", name, left, err)
		}
		second := pinnedInfo(t, filepath.Join(dir, name))
		if os.SameFile(first, second) {
			t.Errorf("%s: the second write is the old file, want a new file", name)
		}
		if got := mustRead(t, filepath.Join(dir, name)); got != "two\n" {
			t.Errorf("%s = %q", name, got)
		}
	}
	if got := entryNames(t, filepath.Join(dir, "info")); !slices.Equal(got, []string{"exclude"}) {
		t.Errorf("info holds %v, want the copy alone", got)
	}

	// A rename that fails leaves the temporary file, and the caller gets its
	// name: `.tmp-` and decimal digits, in the folder of the copy.
	if err := os.MkdirAll(filepath.Join(dir, "info", "attributes", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	left, err := writeNewFile(root, filepath.Join("info", "attributes"), []byte("x"))
	if err == nil {
		t.Fatal("a rename over a folder with an entry worked")
	}
	if !regexp.MustCompile(`^info[/\\]\.tmp-[0-9]{1,10}$`).MatchString(left) {
		t.Errorf("the temporary file = %q, want info/.tmp-<digits>", left)
	}
	if fi, err := os.Lstat(filepath.Join(dir, left)); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("the temporary file: %v", err)
	} else if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("the temporary file has mode %o, want 0600", fi.Mode().Perm())
	}
}

// TestShadowTestCopyIsWrittenAsNewFiles: the second texts of the folder
// `shadow` of the start test are new files (val-s3p2b2-3-linux2 row D5: a new
// inode number for config and HEAD in 14 of 14 cells, Linux VM).
func TestShadowTestCopyIsWrittenAsNewFiles(t *testing.T) {
	base := resolveTestRoot(t, t.TempDir())
	gitDir, shadowDir := filepath.Join(base, "real", ".git"), filepath.Join(base, "shadow")
	mustWrite(t, filepath.Join(gitDir, "HEAD"), "ref: refs/heads/master\n")
	if err := makeShadowTestCopy(gitDir, shadowDir); err != nil {
		t.Fatal(err)
	}
	first := map[string]os.FileInfo{}
	for _, name := range []string{"HEAD", "config"} {
		first[name] = pinnedInfo(t, filepath.Join(shadowDir, name))
	}
	if got := mustRead(t, filepath.Join(shadowDir, "HEAD")); got != shadowTestFirstHead {
		t.Fatalf("the first HEAD = %q", got)
	}
	if err := fillShadowTestCopy(gitDir, shadowDir, []byte("core.bare\nfalse\x00")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"HEAD", "config"} {
		if os.SameFile(first[name], pinnedInfo(t, filepath.Join(shadowDir, name))) {
			t.Errorf("%s: the second text is in the old file, want a new file", name)
		}
	}
	if got, want := mustRead(t, filepath.Join(shadowDir, "config")), "[core]\n\tbare = \"false\"\n"+shadowConfigTail+shadowTestHelper; got != want {
		t.Errorf("the second config = %q, want %q", got, want)
	}
	if got := mustRead(t, filepath.Join(shadowDir, "HEAD")); got != "ref: refs/heads/master\n" {
		t.Errorf("the second HEAD = %q", got)
	}
	for _, name := range entryNames(t, shadowDir) {
		if strings.HasPrefix(name, ".tmp-") {
			t.Errorf("a temporary file is left: %s", name)
		}
	}
}

// TestShadowStateFilesNeedNoNewConfig: when `shallow`, `info/attributes` or
// `info/exclude` comes, changes or goes, the copy follows and the config of the
// copy stays the same file. 5fd08069 makes no read of the config then (cells
// 19a to 19c of val-s3p2b2-3-linux, Linux VM. Cells 15-19a to 15-19c of
// val-s3p2b2-3-macos. Cell E15b of val-s3p2b2-3-windows).
func TestShadowStateFilesNeedNoNewConfig(t *testing.T) {
	f := newRepoFolderFixture(t)
	dir := f.sync(t, f.repo)
	config := pinnedInfo(t, filepath.Join(dir, "config"))
	read := repoReadVerdict(gitDirTrustFor(f.repo), nil)
	for _, row := range []struct{ file, text string }{
		{"shallow", strings.Repeat("a", 40) + "\n"},
		{"info/attributes", "*.bin binary\n"},
		{"info/exclude", "*.o\n"},
	} {
		src := filepath.Join(f.gitDir, filepath.FromSlash(row.file))
		for _, step := range []string{"comes", "changes", "goes"} {
			switch step {
			case "comes":
				mustWrite(t, src, row.text)
			case "changes":
				mustWrite(t, src, row.text+row.text)
				laterTime(t, src)
			default:
				if err := os.Remove(src); err != nil {
					t.Fatal(err)
				}
			}
			f.sync(t, f.repo)
			if now := repoReadVerdict(gitDirTrustFor(f.repo), nil); now != read {
				t.Errorf("%s %s: the daemon read the config again", row.file, step)
				read = now
			}
			if !os.SameFile(config, pinnedInfo(t, filepath.Join(dir, "config"))) {
				t.Errorf("%s %s: the config of the copy is a new file", row.file, step)
				config = pinnedInfo(t, filepath.Join(dir, "config"))
			}
			_, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(row.file)))
			if (step == "goes") != os.IsNotExist(err) {
				t.Errorf("%s %s: the copy: %v", row.file, step, err)
			}
		}
	}
	// A refused file is still refused in the request that meets it.
	mustWrite(t, filepath.Join(f.gitDir, "shallow"), "not an id\n")
	if msg, _ := repoConfigGate(gitDirTrustFor(f.repo), false, nil); !strings.Contains(msg, "holds something other than object ids") {
		t.Errorf("the gate with a bad shallow file = %q", msg)
	}
}

// TestGitInfoAsksForTheGitDirWhereNoRepositoryIs: in a folder with no
// repository git.info runs its listing and then the heavy `rev-parse
// --git-dir`, both with GIT_DIR=<null device>. 5fd08069 makes that second call
// (val-s3p2b2-3-macos cells S1-first-norepo, macOS VM. val-s3p2b2-3-windows row
// D6, Windows VM). The reply is the "no repository" answer.
func TestGitInfoAsksForTheGitDirWhereNoRepositoryIs(t *testing.T) {
	f := newSeededShadowFixture(t)
	plain := filepath.Join(resolveTestRoot(t, t.TempDir()), "norepo")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, extra := range []map[string]any{nil, {"shadowDirOff": true}} {
		raw, calls := f.info(t, plain, extra)
		if !strings.Contains(raw, `"result":{"isRepo":false,"repoSlug":"","defaultBranch":""`) {
			t.Fatalf("git.info = %s", raw)
		}
		var got []string
		for _, c := range calls {
			if c.excludesRead() {
				continue
			}
			if c.cwd != plain {
				t.Errorf("call %q runs in %s, want %s", c.argv, c.cwd, plain)
			}
			if v, _ := lookupExact(c.env, "GIT_DIR"); v != os.DevNull {
				t.Errorf("call %q has GIT_DIR=%q, want the null device", c.argv, v)
			}
			n := len(c.argv)
			got = append(got, strings.Join(c.argv[max(0, n-2):], " "))
		}
		if want := []string{"-z --list", "rev-parse --git-dir"}; !slices.Equal(got, want) {
			t.Errorf("the calls end %q, want %q (member: %v)", got, want, extra != nil)
		}
		if last := calls[len(calls)-1]; !last.hardened() || len(last.pins(t)) != 2 {
			t.Errorf("the rev-parse call %q has no option list or no hook pins", last.argv)
		}
	}
}

// TestGitInfoWithNoShortHead: when `rev-parse --short HEAD` fails, the reply
// has no `branch` member. On a Windows VM 5fd08069 answers so for a files
// repository with `extensions.refStorage = reftable` (cell V6-13d of
// val-s3p2b2-3-windows). Here the stub fails that one call.
func TestGitInfoWithNoShortHead(t *testing.T) {
	f := newSeededShadowFixture(t)
	runGit(t, f.repo, "checkout", "-q", "--detach")
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"branch":"detached:`) {
		t.Fatalf("git.info on a detached HEAD = %s", raw)
	}
	slowGit(t, "rev-parse,--short,HEAD", "fail", 0, "fatal: Needed a single revision\n", "")
	t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "128")
	raw, _ := f.info(t, f.repo, nil)
	want := `"result":{"isRepo":true,"repo":"repo","root":` + jsonQuoted(t, filepath.ToSlash(f.repo)) + `,"repoSlug":"","defaultBranch":"","settingsControl":"shadow"}`
	if !strings.Contains(strings.ReplaceAll(raw, `\\`, `/`), strings.ReplaceAll(want, `\\`, `/`)) {
		t.Errorf("git.info = %s\nwant %s", raw, want)
	}
}

// TestShadowFallbackKeepsItsFoldersInTheDaemonFolder: in the state fallback-git
// a git.status has its temporary git folder in the folder of the daemon, and a
// create its index folder. With the member both are in the temporary folder
// (val-s3p2b2-3-windows cell Q-S4c, Windows VM. val-s3p2b2-1-macos cell
// S4-wt1-first-status, macOS VM).
func TestShadowFallbackKeepsItsFoldersInTheDaemonFolder(t *testing.T) {
	f := newSeededShadowFixture(t)
	h := f.s.gitShadow
	h.mu.Lock()
	h.state = controlFallback
	err := h.makeFolder()
	dir := h.dir
	h.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	valueWith := func(calls []envCall, prefix string) string {
		for _, c := range calls {
			for _, kv := range slices.Concat(c.argv, c.env) {
				if rest, ok := strings.CutPrefix(kv, prefix); ok {
					return rest
				}
			}
		}
		return ""
	}
	sep := string(filepath.Separator)
	raw, calls := f.call(t, "git.status", map[string]any{"path": f.wt, "baseRepo": f.repo})
	if !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("git.status = %s", raw)
	}
	if got := valueWith(calls, "--git-dir="+dir+sep); !regexp.MustCompile(`^\.gitdir-[0-9]+$`).MatchString(got) {
		t.Errorf("the temporary git folder of git.status = %q, want .gitdir-<n> in the folder of the daemon", got)
	}
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, calls = f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s", raw)
	}
	if got := valueWith(calls, "GIT_INDEX_FILE="+dir+sep); !regexp.MustCompile(`^\.index-[0-9]+[/\\]index$`).MatchString(got) {
		t.Errorf("GIT_INDEX_FILE of the checkout = %q, want .index-<n>/index in the folder of the daemon", got)
	}
	// The light calls of the create carry six values, and no call names a copy.
	for _, c := range calls {
		if v, _ := lookupExact(c.env, "GIT_COMMON_DIR"); v != f.gitDir {
			t.Errorf("call %q has GIT_COMMON_DIR=%q, want the git directory", c.argv, v)
		}
		if slices.Contains(c.argv, "--is-inside-work-tree") {
			if v, _ := lookupExact(c.env, "GIT_ALLOW_PROTOCOL"); v != "denied_by_claude_ssh" {
				t.Errorf("the light call %q has GIT_ALLOW_PROTOCOL=%q", c.argv, v)
			}
			if _, ok := lookupExact(c.env, "GIT_NO_LAZY_FETCH"); !ok {
				t.Errorf("the light call %q has no GIT_NO_LAZY_FETCH", c.argv)
			}
		}
	}
	if got, want := entryNames(t, dir), []string{shadowLockName}; !slices.Equal(got, want) {
		t.Errorf("after the requests the folder of the daemon holds %v, want %v", got, want)
	}
	// With the member the folder of the daemon is not used.
	_, calls = f.call(t, "git.status", map[string]any{"path": f.wt, "baseRepo": f.repo, "shadowDirOff": true})
	if got := valueWith(calls, "--git-dir="+dir+sep); got != "" {
		t.Errorf("with the member git.status names %q in the folder of the daemon", got)
	}
}
