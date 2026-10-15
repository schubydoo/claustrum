package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// The tests of the daemon's own read of a repository's config (gitrepoconfig.go).
// The cell names are those of the VM rounds. 5fd08069
// answered each text on a Linux VM, unless a comment names another system.

// baseRepoConfig is the config that `git init` writes on Linux, without the
// version line.
const baseRepoConfig = "\tfilemode = true\n\tbare = false\n\tlogallrefupdates = true\n"

// configRepo makes a repository with one commit and returns its folder and its
// git directory, both with symlinks resolved.
func configRepo(t *testing.T) (repo, gitDir string) {
	t.Helper()
	requireGit(t)
	repo = filepath.Join(resolveTestRoot(t, t.TempDir()), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	return repo, filepath.Join(repo, ".git")
}

// setRepoConfig writes text as the config of gitDir.
func setRepoConfig(t *testing.T, gitDir, text string) {
	t.Helper()
	setIncludeFile(t, gitDir, "config", text)
}

// setIncludeFile writes text as the file name of gitDir.
func setIncludeFile(t *testing.T, gitDir, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(gitDir, name), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// versionConfig is a config with the format version line as given.
func versionConfig(versionLine string) string {
	return "[core]\n\t" + versionLine + "\n" + baseRepoConfig
}

// version0 is the config of a `git init` on Linux: format version 0 and three
// more lines.
var version0 = versionConfig("repositoryformatversion = 0")

func configDetail(t *testing.T, gitDir string) string {
	t.Helper()
	return repoConfigDetail(context.Background(), gitDir, "", nil)
}

func versionDetail(v string) string {
	return "the repository uses format version " + strconv.Quote(v) + ", which this version of Claude does not support"
}

func extensionDetail(name string) string {
	return "the repository uses git extension " + strconv.Quote(name) +
		", or a value of it, that this version of Claude does not support"
}

const includeLimitDetail = "its includes go past what Claude follows (16 files, nested 3 deep)"

// breakListing makes the configuration listing of git fail in the repository of
// gitDir: the config has an `include.path` setting with no value. The daemon's
// own read gives no refusal for that setting, and git ends its listing with
// "missing value for 'include.path'" and a "bad config line" text that names
// the config. So the listing of the method answers with the hooks refusal, as
// 89cb6289 does (cell X9-04, Linux VM). A syntax error gets the text of the
// read, in the config and in an included file (TestRepoConfigSyntaxError).
func breakListing(t *testing.T, gitDir string) {
	t.Helper()
	cfg := filepath.Join(gitDir, "config")
	b, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, cfg, string(b)+"[include]\n\tpath\n", 0o644)
}

// refusedInclude is a config file with a format version that the read refuses
// in the config itself.
const refusedInclude = "[core]\n\trepositoryformatversion = 2\n"

// markerInclude is a config file that the read refuses when it follows it: its
// own include is one that is not expanded.
const markerInclude = "[include]\n\tpath = %(prefix)/x\n"

// markerDetail is the refusal of markerInclude at path.
func markerDetail(path string) string {
	return strconv.Quote(path) + ` includes "%(prefix)/x", and Claude expands neither ~user nor %(prefix), ` +
		`nor ~ where no home directory is set; write that path out in full`
}

// TestRepoConfigFormatVersion pins the format version rule (cells C2, E8 and
// X7). The value is the text that git lists, and the config itself decides.
func TestRepoConfigFormatVersion(t *testing.T) {
	_, gitDir := configRepo(t)
	setIncludeFile(t, gitDir, "inc.cfg", refusedInclude)
	for _, c := range []struct{ name, config, want string }{
		{"0", version0, ""},
		{"1", versionConfig("repositoryformatversion = 1"), ""},
		{"1 in quotes", versionConfig(`repositoryformatversion = "1"`), ""},
		{"1 after spaces", versionConfig("repositoryformatversion =    1"), ""},
		{"upper case key, 1", versionConfig("REPOSITORYFORMATVERSION = 1"), ""},
		{"2", versionConfig("repositoryformatversion = 2"), versionDetail("2")},
		{"upper case key, 2", versionConfig("REPOSITORYFORMATVERSION = 2"), versionDetail("2")},
		{"upper case section, 2", "[CORE]\n\tREPOSITORYFORMATVERSION = 2\n", versionDetail("2")},
		{"quoted space 0", versionConfig(`repositoryformatversion = " 0"`), versionDetail(" 0")},
		{"00", versionConfig("repositoryformatversion = 00"), versionDetail("00")},
		{"01", versionConfig("repositoryformatversion = 01"), versionDetail("01")},
		{"+1", versionConfig("repositoryformatversion = +1"), versionDetail("+1")},
		{"0x1", versionConfig("repositoryformatversion = 0x1"), versionDetail("0x1")},
		{"1k", versionConfig("repositoryformatversion = 1k"), versionDetail("1k")},
		{"true", versionConfig("repositoryformatversion = true"), versionDetail("true")},
		{"-1", versionConfig("repositoryformatversion = -1"), versionDetail("-1")},
		{"1.0", versionConfig("repositoryformatversion = 1.0"), versionDetail("1.0")},
		{"empty", versionConfig("repositoryformatversion ="), versionDetail("")},
		{"no value", versionConfig("repositoryformatversion"), versionDetail("")},
		{"0 then 2", versionConfig("repositoryformatversion = 0\n\trepositoryformatversion = 2"), versionDetail("2")},
		{"2 then 0", versionConfig("repositoryformatversion = 2\n\trepositoryformatversion = 0"), ""},
		{"no line", "[core]\n" + baseRepoConfig, ""},
		// The version of an included file does not count (cell X7).
		{"0, and 2 in an included file", version0 + "[include]\n\tpath = inc.cfg\n", ""},
	} {
		setRepoConfig(t, gitDir, c.config)
		if got := configDetail(t, gitDir); got != c.want {
			t.Errorf("version %s: detail = %q, want %q", c.name, got, c.want)
		}
	}
	// The value in the text is cut to its first 20 characters, with no mark. Cells
	// N11 ran 100, 101, 300, 301 and 1100 characters (Linux and Windows VMs). The
	// pair 20 and 21 is from the code.
	for _, n := range []int{20, 21, 100, 1100} {
		v := strings.Repeat("7", n)
		setRepoConfig(t, gitDir, versionConfig("repositoryformatversion = "+v))
		if got, want := configDetail(t, gitDir), versionDetail(v[:20]); got != want {
			t.Errorf("version of %d characters: detail = %q, want %q", n, got, want)
		}
	}
}

// TestRepoConfigExtensions pins the extension rule (cells C2, E9, A3, X8 and
// X9). The name in the text is in lower case. A sha256 repository and a
// reftable repository are not refused.
func TestRepoConfigExtensions(t *testing.T) {
	_, gitDir := configRepo(t)
	setIncludeFile(t, gitDir, "inc.cfg", "[extensions]\n\tfoo = true\n")
	for _, c := range []struct{ version, line, want string }{
		{"1", "foo = false", extensionDetail("foo")},
		{"1", "foo", extensionDetail("foo")},
		{"1", "foo = true", extensionDetail("foo")},
		{"1", "refStorage = banana", extensionDetail("refstorage")},
		{"1", "objectFormat = md5", extensionDetail("objectformat")},
		{"1", "OBJECTFORMAT = md5", extensionDetail("objectformat")},
		{"1", "partialClone", extensionDetail("partialclone")},
		{"1", "preciousObjects = maybe", extensionDetail("preciousobjects")},
		{"1", "objectFormat = sha1", ""},
		{"1", "objectFormat = sha256", ""},
		{"1", "compatObjectFormat = sha256", ""},
		{"1", "refStorage = files", ""},
		{"1", "refStorage = reftable", ""},
		{"1", "worktreeConfig = true", ""},
		{"1", "worktreeConfig = false", ""},
		{"1", "relativeWorktrees = true", ""},
		{"1", "submodulePathConfig = true", ""},
		{"1", "noop-v1 = true", ""},
		{"1", "noop = true", ""},
		{"1", "noop = 2", ""},
		{"1", "preciousObjects = true", ""},
		{"1", "partialClone = origin", ""},
		// Cells V6, on Linux, macOS and Windows VMs.
		{"1", "bar = true", extensionDetail("bar")},
		{"1", "noop2", extensionDetail("noop2")},
		{"1", "relativeWorktrees = maybe", extensionDetail("relativeworktrees")},
		{"1", "submodulePathConfig = maybe", extensionDetail("submodulepathconfig")},
		{"1", "noop-v1 = maybe", extensionDetail("noop-v1")},
		{"1", "compatObjectFormat = md5", extensionDetail("compatobjectformat")},
		{"1", "partialClone =", extensionDetail("partialclone")},
		{"1", "worktreeConfig = maybe", ""},
		{"1", "objectFormat = SHA256", ""},
		{"1", "refStorage = FILES", ""},
		{"1", "preciousObjects = yes", ""},
		{"1", "preciousObjects = 1", ""},
		// Not measured: another value of a known name. claustrum does not refuse it.
		{"1", "partialClone = upstream", ""},
		{"0", "foo = true", ""},
		{"0", "objectformat = md5", ""},
		{"0", "partialClone", ""},
		{"0", "preciousObjects = maybe", ""},
		{"0", "worktreeConfig = true", ""},
	} {
		setRepoConfig(t, gitDir, versionConfig("repositoryformatversion = "+c.version)+"[extensions]\n\t"+c.line+"\n")
		if got := configDetail(t, gitDir); got != c.want {
			t.Errorf("version %s, extensions.%s: detail = %q, want %q", c.version, c.line, got, c.want)
		}
	}
	// An extension of an included file does not count (cell X9).
	setRepoConfig(t, gitDir, versionConfig("repositoryformatversion = 1")+"[include]\n\tpath = inc.cfg\n")
	if got := configDetail(t, gitDir); got != "" {
		t.Errorf("extensions.foo in an included file: detail = %q, want none", got)
	}
	// An extension in a section with a subsection has both in its name (cells V6).
	setRepoConfig(t, gitDir, versionConfig("repositoryformatversion = 1")+"[extensions \"x\"]\n\tnoop = true\n")
	if got, want := configDetail(t, gitDir), extensionDetail("x.noop"); got != want {
		t.Errorf(`[extensions "x"] noop: detail = %q, want %q`, got, want)
	}
	// The name in the text is cut to its first 60 characters, with no mark. Cells
	// N11 ran 100, 101, 300, 301 and 900 characters (Linux and Windows VMs). The
	// pair 60 and 61 is from the code.
	for _, n := range []int{60, 61, 100, 900} {
		name := strings.Repeat("z", n)
		setRepoConfig(t, gitDir, versionConfig("repositoryformatversion = 1")+"[extensions]\n\t"+name+" = true\n")
		if got, want := configDetail(t, gitDir), extensionDetail(name[:60]); got != want {
			t.Errorf("extension name of %d characters: detail = %q, want %q", n, got, want)
		}
	}
	// With no version key no extension is refused (cells V6).
	setRepoConfig(t, gitDir, "[core]\n"+baseRepoConfig+"[extensions]\n\tfoo = true\n")
	if got := configDetail(t, gitDir); got != "" {
		t.Errorf("extensions.foo with no version key: detail = %q, want none", got)
	}
}

// TestRepoConfigIncludeRow pins the bound on the count of included files: 15 and
// 16 in a row pass, and 17 are refused (cells E10). With 16 in a row, one more
// file that the last one includes is refused too (cell X9).
func TestRepoConfigIncludeRow(t *testing.T) {
	for _, c := range []struct {
		n      int
		nested bool
		want   string
	}{{15, false, ""}, {16, false, ""}, {17, false, includeLimitDetail}, {16, true, includeLimitDetail}} {
		_, gitDir := configRepo(t)
		config := version0
		for i := range c.n {
			name := fmt.Sprintf("r%02d.cfg", i)
			config += "[include]\n\tpath = " + name + "\n"
			text := fmt.Sprintf("[x]\n\tr%02d = 1\n", i)
			if c.nested && i == c.n-1 {
				text += "[include]\n\tpath = more.cfg\n"
			}
			setIncludeFile(t, gitDir, name, text)
		}
		setIncludeFile(t, gitDir, "more.cfg", "[x]\n\tmore = 1\n")
		setRepoConfig(t, gitDir, config)
		if got := configDetail(t, gitDir); got != c.want {
			t.Errorf("%d includes in a row, nested %v: detail = %q, want %q", c.n, c.nested, got, c.want)
		}
	}
}

// TestRepoConfigIncludeCount pins what counts toward the 16 files (cells V8,
// Linux, macOS and Windows VMs). A missing file counts: 17 missing files are
// refused (the three systems), and 16 pass (cells V8, Linux VM, and P8w, Windows
// VM). More than 16 are refused in the two orders with files that exist that
// ran. The file of an
// `includeIf` section counts too: 15 files and one such file pass, and 16 and
// one are refused.
func TestRepoConfigIncludeCount(t *testing.T) {
	repo, gitDir := configRepo(t)
	for i := range 16 {
		setIncludeFile(t, gitDir, fmt.Sprintf("r%02d.cfg", i), fmt.Sprintf("[x]\n\tr%02d = 1\n", i))
	}
	setIncludeFile(t, gitDir, "im.cfg", "[x]\n\tim = 1\n")
	lines := func(prefix string, n int) string {
		var b strings.Builder
		for i := range n {
			fmt.Fprintf(&b, "\tpath = %s%02d.cfg\n", prefix, i)
		}
		return b.String()
	}
	includeIf := "[includeIf \"gitdir:" + filepath.ToSlash(repo) + "/\"]\n\tpath = im.cfg\n"
	for _, c := range []struct{ name, added, want string }{
		{"16 missing", "[include]\n" + lines("m", 16), ""},
		{"17 missing", "[include]\n" + lines("m", 17), includeLimitDetail},
		{"16 files, then 5 missing", "[include]\n" + lines("r", 16) + lines("m", 5), includeLimitDetail},
		{"5 missing, then 16 files", "[include]\n" + lines("m", 5) + lines("r", 16), includeLimitDetail},
		{"15 files and an includeIf file", "[include]\n" + lines("r", 15) + includeIf, ""},
		{"16 files and an includeIf file", "[include]\n" + lines("r", 16) + includeIf, includeLimitDetail},
	} {
		setRepoConfig(t, gitDir, version0+c.added)
		if got := configDetail(t, gitDir); got != c.want {
			t.Errorf("%s: detail = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestRepoConfigIncludeChain pins the bound on the depth of a chain: config, d1,
// d2 and d3 pass, and a fourth level is refused (cells E10). A loop of two files
// (cell C3), a config that includes itself and an included file that includes
// itself (cells X9) are refused too.
func TestRepoConfigIncludeChain(t *testing.T) {
	for _, c := range []struct {
		depth int
		want  string
	}{{2, ""}, {3, ""}, {4, includeLimitDetail}} {
		_, gitDir := configRepo(t)
		setRepoConfig(t, gitDir, version0+"[include]\n\tpath = d1.cfg\n")
		for i := 1; i <= c.depth; i++ {
			text := fmt.Sprintf("[x]\n\td%d = 1\n", i)
			if i < c.depth {
				text += fmt.Sprintf("[include]\n\tpath = d%d.cfg\n", i+1)
			}
			setIncludeFile(t, gitDir, fmt.Sprintf("d%d.cfg", i), text)
		}
		if got := configDetail(t, gitDir); got != c.want {
			t.Errorf("chain of depth %d: detail = %q, want %q", c.depth, got, c.want)
		}
	}
	_, gitDir := configRepo(t)
	setIncludeFile(t, gitDir, "a.cfg", "[include]\n\tpath = b.cfg\n")
	setIncludeFile(t, gitDir, "b.cfg", "[include]\n\tpath = a.cfg\n")
	setIncludeFile(t, gitDir, "self.cfg", "[include]\n\tpath = self.cfg\n")
	for _, value := range []string{"a.cfg", "config", "self.cfg"} {
		setRepoConfig(t, gitDir, version0+"[include]\n\tpath = "+value+"\n")
		if got := configDetail(t, gitDir); got != includeLimitDetail {
			t.Errorf("include loop through %s: detail = %q, want %q", value, got, includeLimitDetail)
		}
	}
}

// TestRepoConfigIncludeFollowed pins which includes the read follows. A relative
// include inside the git directory is followed. A missing file, an empty value
// and a setting with no value are no refusal (cells C3 and X9). The file of an
// `includeIf` section is not read: a version `2` in it is not refused, with a
// `gitdir:` condition that matches and with `onbranch:` (cells E10 and X9). An
// include line in such a file is not followed (not measured).
func TestRepoConfigIncludeFollowed(t *testing.T) {
	repo, gitDir := configRepo(t)
	setIncludeFile(t, gitDir, "inc.cfg", markerInclude)
	setIncludeFile(t, gitDir, "v2.cfg", refusedInclude)

	setRepoConfig(t, gitDir, version0+"[include]\n\tpath = inc.cfg\n")
	if got, want := configDetail(t, gitDir), markerDetail(filepath.Join(gitDir, "inc.cfg")); got != want {
		t.Errorf("relative include: detail = %q, want %q", got, want)
	}
	for _, c := range []struct{ name, section string }{
		{"missing include", "[include]\n\tpath = missing.cfg\n"},
		{"empty include", "[include]\n\tpath =\n"},
		{"include with no value", "[include]\n\tpath\n"},
		{"includeIf gitdir that matches, version 2", "[includeIf \"gitdir:" + filepath.ToSlash(repo) + "/\"]\n\tpath = v2.cfg\n"},
		{"includeIf gitdir that matches, marker", "[includeIf \"gitdir:" + filepath.ToSlash(repo) + "/\"]\n\tpath = inc.cfg\n"},
		{"includeIf onbranch, version 2", "[includeIf \"onbranch:main\"]\n\tpath = v2.cfg\n"},
	} {
		setRepoConfig(t, gitDir, version0+c.section)
		if got := configDetail(t, gitDir); got != "" {
			t.Errorf("%s: detail = %q, want none", c.name, got)
		}
	}
}

// TestRepoConfigIncludeIfFileTests pins that the file of an `includeIf` section
// gets the size test, with its own path, whatever the condition is: `gitdir:`
// that matches (cell X9) or not, `gitdir/i:`, `onbranch:` and `hasconfig:`
// (cells V8, Linux, macOS and Windows VMs).
func TestRepoConfigIncludeIfFileTests(t *testing.T) {
	repo, gitDir := configRepo(t)
	big := filepath.Join(gitDir, "im.cfg")
	if err := os.WriteFile(big, []byte(strings.Repeat("#c\n", (repoConfigMaxBytes+3)/3)), 0o644); err != nil {
		t.Fatal(err)
	}
	slashed := filepath.ToSlash(repo)
	for _, cond := range []string{"gitdir:" + slashed + "/", "gitdir:/nonexistent/zz/", "gitdir/i:" + strings.ToUpper(slashed) + "/",
		"onbranch:main", "hasconfig:remote.*.url:*"} {
		setRepoConfig(t, gitDir, version0+"[includeIf \""+cond+"\"]\n\tpath = im.cfg\n")
		if got, want := configDetail(t, gitDir), strconv.Quote(big)+" is larger than 8 MiB"; got != want {
			t.Errorf("includeIf %s, large file: detail = %q, want %q", cond, got, want)
		}
	}
}

// keyNameDetail is the refusal of a setting name over the bound.
func keyNameDetail(gitDir, section string) string {
	return strconv.Quote(filepath.Join(gitDir, "config")) + " has a setting in section " + strconv.Quote(section) +
		" whose name is longer than 1024 bytes"
}

// TestRepoConfigKeyName pins the bound on a setting name (cells E2, E3, X4, Y4
// and V4). The full name with its section and subsection has 1024 bytes at the
// last accepted cell and 1025 at the first refused one. That pair is measured in
// core and remote, and in lfs, url, filter, extensions, include and includeif.
// Cells N6 measured the same pair in index, feature and checkout (Linux, macOS
// and Windows VMs). The bound
// holds in the measured sections only, and the text names the section in lower
// case. It holds in an included file too (cells V7).
func TestRepoConfigKeyName(t *testing.T) {
	_, gitDir := configRepo(t)
	for _, c := range []struct {
		header  string
		key     int // the bytes of the key after the section and subsection
		section string
	}{
		{"[core]", 1019, ""},
		{"[core]", 1020, "core"},
		{`[remote "o"]`, 1015, ""},
		{`[remote "o"]`, 1016, "remote"},
		{`[remote "` + strings.Repeat("s", 100) + `"]`, 916, ""},
		{`[remote "` + strings.Repeat("s", 100) + `"]`, 917, "remote"},
		{"[extensions]", 1100, "extensions"},
		{"[include]", 1100, "include"},
		{"[lfs]", 1100, "lfs"},
		{`[lfs "l"]`, 1100, "lfs"},
		{`[url "u"]`, 1100, "url"},
		{`[filter "f"]`, 1100, "filter"},
		{`[includeIf "gitdir:/x/"]`, 1100, "includeif"},
		// The exact pairs of cells V4: the full name has 1024 and 1025 bytes.
		{"[lfs]", 1020, ""},
		{"[lfs]", 1021, "lfs"},
		{`[url "u"]`, 1018, ""},
		{`[url "u"]`, 1019, "url"},
		{`[filter "f"]`, 1015, ""},
		{`[filter "f"]`, 1016, "filter"},
		{"[extensions]", 1013, ""},
		{"[extensions]", 1014, "extensions"},
		{"[include]", 1016, ""},
		{"[include]", 1017, "include"},
		{`[includeIf "gitdir:/x/"]`, 1003, ""},
		{`[includeIf "gitdir:/x/"]`, 1004, "includeif"},
		{"[index]", 1100, "index"},
		{"[feature]", 1100, "feature"},
		{"[checkout]", 1100, "checkout"},
		// The exact pairs of cells N6.
		{"[index]", 1018, ""},
		{"[index]", 1019, "index"},
		{"[feature]", 1016, ""},
		{"[feature]", 1017, "feature"},
		{"[checkout]", 1015, ""},
		{"[checkout]", 1016, "checkout"},
		{`[branch "` + strings.Repeat("s", 1100) + `"]`, 6, ""},
		{`[CORE "o"]`, 1100, "core"},
		{"[user]", 1100, ""},
		{"[alias]", 1100, ""},
		{"[http]", 1100, ""},
		{"[credential]", 1100, ""},
		{"[safe]", 1100, ""},
		{"[gc]", 1100, ""},
		{"[diff]", 1100, ""},
		{"[merge]", 1100, ""},
		{"[x]", 1100, ""},
		{"[abcde]", 2200, ""},
		{`[branch "b"]`, 1100, ""},
		{`[submodule "s"]`, 1100, ""},
		{`[x "o"]`, 1100, ""},
		{"[" + strings.Repeat("n", 1100) + "]", 3, ""},
	} {
		setRepoConfig(t, gitDir, version0+c.header+"\n\t"+strings.Repeat("k", c.key)+" = 1\n")
		want := ""
		if c.section != "" {
			want = keyNameDetail(gitDir, c.section)
		}
		if got := configDetail(t, gitDir); got != want {
			t.Errorf("%.20s key of %d bytes: detail = %q, want %q", c.header, c.key, got, want)
		}
	}
	// The subsection counts: 1013 bytes pass with the key `url`, and 1014 do not.
	for n, want := range map[int]string{1013: "", 1014: keyNameDetail(gitDir, "remote"), 70000: keyNameDetail(gitDir, "remote")} {
		setRepoConfig(t, gitDir, version0+`[remote "`+strings.Repeat("s", n)+`"]`+"\n\turl = x\n")
		if got := configDetail(t, gitDir); got != want {
			t.Errorf("subsection of %d bytes: detail = %q, want %q", n, got, want)
		}
	}
	// A value of 5000 bytes passes (cell C4).
	setRepoConfig(t, gitDir, version0+"[core]\n\tk = "+strings.Repeat("v", 5000)+"\n")
	if got := configDetail(t, gitDir); got != "" {
		t.Errorf("value of 5000 bytes: detail = %q, want none", got)
	}
	// A long name in an included file is refused with the path of that file (cells
	// V7, Linux, macOS and Windows VMs).
	setIncludeFile(t, gitDir, "inc.cfg", "[core]\n\t"+strings.Repeat("k", 1100)+" = 1\n")
	setRepoConfig(t, gitDir, version0+"[include]\n\tpath = inc.cfg\n")
	want := strings.Replace(keyNameDetail(gitDir, "core"), strconv.Quote(filepath.Join(gitDir, "config")),
		strconv.Quote(filepath.Join(gitDir, "inc.cfg")), 1)
	if got := configDetail(t, gitDir); got != want {
		t.Errorf("long name in an included file: detail = %q, want %q", got, want)
	}
}

// TestRepoConfigSettingsCost pins each measured pair of cells X5: the count of
// lines after the four lines of a `git init` config, at the last accepted cell
// and at the first refused one. The arithmetic of repoSettingCost is fitted to
// these pairs.
//
// The test runs with the parallel tests: it has 22 large configs, and it uses no
// state of the process beyond the real git.
func TestRepoConfigSettingsCost(t *testing.T) {
	t.Parallel()
	_, gitDir := configRepo(t)
	refused := strconv.Quote(filepath.Join(gitDir, "config")) + " has more settings than Claude reads (8 MiB); remove repeated ones"
	repeat := func(lines ...string) func(n int) string {
		return func(n int) string {
			var b strings.Builder
			for i := range n {
				b.WriteString(lines[i%len(lines)])
			}
			return b.String()
		}
	}
	distinct := func(section string) func(n int) string {
		return func(n int) string {
			var b strings.Builder
			b.WriteString(section)
			for i := range n {
				// Five letters a to j, so each key has 5 bytes and no two are equal.
				key := []byte(fmt.Sprintf("%05d", i))
				for j := range key {
					key[j] += 'a' - '0'
				}
				b.Write(key)
				b.WriteString("=1\n")
			}
			return b.String()
		}
	}
	for _, c := range []struct {
		name    string
		added   func(n int) string
		n       int
		refused bool
	}{
		{"core.filemode = true", repeat("[core]\n\tfilemode = true\n"), 74231, false},
		{"core.filemode = true", repeat("[core]\n\tfilemode = true\n"), 74232, true},
		{"core.filemode = <100 bytes>", repeat("[core]\n\tfilemode = " + strings.Repeat("v", 100) + "\n"), 40134, false},
		{"core.filemode = <100 bytes>", repeat("[core]\n\tfilemode = " + strings.Repeat("v", 100) + "\n"), 40135, true},
		{"core.filemode and core.bare in turn", repeat("[core]\n\tfilemode = true\n", "[core]\n\tbare = false\n"), 75229, false},
		{"core.filemode and core.bare in turn", repeat("[core]\n\tfilemode = true\n", "[core]\n\tbare = false\n"), 75230, true},
		{"remote.origin.url = abcd", repeat("[remote \"origin\"]\n\turl = abcd\n"), 71693, false},
		{"remote.origin.url = abcd", repeat("[remote \"origin\"]\n\turl = abcd\n"), 71694, true},
		{"core.bare = false", repeat("[core]\n\tbare = false\n"), 76255, false},
		{"core.bare = false", repeat("[core]\n\tbare = false\n"), 76256, true},
		{"distinct keys in core", distinct("[core]\n"), 78393, false},
		{"distinct keys in core", distinct("[core]\n"), 78394, true},
		{"core.filemode with an empty value", repeat("[core]\n\tfilemode =\n"), 76955, false},
		{"core.filemode with an empty value", repeat("[core]\n\tfilemode =\n"), 76956, true},
		{"core.filemode with no value", repeat("[core]\n\tfilemode\n"), 76955, false},
		{"core.filemode with no value", repeat("[core]\n\tfilemode\n"), 76956, true},
		// 200000 lines: three more counted sections, and two that do not count.
		{"extensions.noop = true", repeat("[extensions]\n\tnoop = true\n"), 200000, true},
		{"include.path of a missing file", repeat("[include]\n\tpath = nosuchfile.cfg\n"), 200000, true},
		{"branch.main.remote = abcd", repeat("[branch \"main\"]\n\tremote = abcd\n"), 200000, true},
		// Cells V5 (Linux VM): seven more counted sections. The 200000 `includeIf`
		// lines name a missing file, and the answer is this text, not the include
		// limit.
		{"lfs.url = abcd", repeat("[lfs]\n\turl = abcd\n"), 200000, true},
		{"index.version = 4", repeat("[index]\n\tversion = 4\n"), 200000, true},
		{"url.u.insteadof = abcd", repeat("[url \"u\"]\n\tinsteadof = abcd\n"), 200000, true},
		{"filter.f.smudge = abcd", repeat("[filter \"f\"]\n\tsmudge = abcd\n"), 200000, true},
		{"feature.manyfiles = true", repeat("[feature]\n\tmanyfiles = true\n"), 200000, true},
		{"checkout.workers = 1", repeat("[checkout]\n\tworkers = 1\n"), 200000, true},
		{"includeIf.gitdir:/x/.path of a missing file", func(n int) string {
			return "[includeIf \"gitdir:/x/\"]\n" + strings.Repeat("\tpath = nosuch.cfg\n", n)
		}, 200000, true},
		{"user.name = abcd", repeat("[user]\n\tname = abcd\n"), 200000, false},
		{"x.y = true", repeat("[x]\n\ty = true\n"), 200000, false},
		{"distinct keys in x", distinct("[x]\n"), 200000, false},
	} {
		setRepoConfig(t, gitDir, version0+c.added(c.n))
		want := ""
		if c.refused {
			want = refused
		}
		if got := configDetail(t, gitDir); got != want {
			t.Errorf("%s, %d lines: detail = %q, want %q", c.name, c.n, got, want)
		}
	}
	// The bound holds in an included file, with the path of that file (cells V7,
	// Linux, macOS and Windows VMs). The included files have their own count: the
	// pair of core.filemode holds there with no line of the config counted. The
	// cells have 200000 lines in one file, so the pair in an included file is from
	// the code.
	setRepoConfig(t, gitDir, version0+"[include]\n\tpath = inc.cfg\n")
	inc := filepath.Join(gitDir, "inc.cfg")
	for n, refusedHere := range map[int]bool{74235: false, 74236: true, 200000: true} {
		setIncludeFile(t, gitDir, "inc.cfg", "[core]\n"+strings.Repeat("\tfilemode = true\n", n))
		want := ""
		if refusedHere {
			want = strconv.Quote(inc) + " has more settings than Claude reads (8 MiB); remove repeated ones"
		}
		if got := configDetail(t, gitDir); got != want {
			t.Errorf("included file with %d core.filemode lines: detail = %q, want %q", n, got, want)
		}
	}
	// The count is one sum across the included files, and the config has its own
	// (cells N5, Linux, macOS and Windows VMs). Each file has 40000 lines, which is
	// under the bound alone. Two included files are refused, and the text names
	// the second one. The config and one included file pass, in both orders.
	half := "[core]\n" + strings.Repeat("\tfilemode = true\n", 40000)
	setIncludeFile(t, gitDir, "i1.cfg", half)
	setIncludeFile(t, gitDir, "i2.cfg", half)
	second := strconv.Quote(filepath.Join(gitDir, "i2.cfg")) + " has more settings than Claude reads (8 MiB); remove repeated ones"
	for _, c := range []struct{ name, config, want string }{
		{"one included file", version0 + "[include]\n\tpath = i1.cfg\n", ""},
		{"two included files", version0 + "[include]\n\tpath = i1.cfg\n\tpath = i2.cfg\n", second},
		{"the config, then an included file", version0 + half + "[include]\n\tpath = i1.cfg\n", ""},
		{"an included file, then the config", version0 + "[include]\n\tpath = i1.cfg\n" + half, ""},
	} {
		setRepoConfig(t, gitDir, c.config)
		if got := configDetail(t, gitDir); got != c.want {
			t.Errorf("40000 lines in each, %s: detail = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestRepoConfigSize pins the size bound of a config file: 8388608 bytes pass,
// and 8388609 are refused (cells X6).
func TestRepoConfigSize(t *testing.T) {
	_, gitDir := configRepo(t)
	for _, c := range []struct {
		size    int
		refused bool
	}{{repoConfigMaxBytes - 1, false}, {repoConfigMaxBytes, false}, {repoConfigMaxBytes + 1, true}, {20 << 20, true}} {
		// A comment line fills the file to its size.
		setRepoConfig(t, gitDir, version0+"#"+strings.Repeat("c", c.size-len(version0)-2)+"\n")
		want := ""
		if c.refused {
			want = strconv.Quote(filepath.Join(gitDir, "config")) + " is larger than 8 MiB"
		}
		if got := configDetail(t, gitDir); got != want {
			t.Errorf("config of %d bytes: detail = %q, want %q", c.size, got, want)
		}
	}
}

// objectIDLines is a text of exactly size bytes that holds object ids only:
// lines of 64 hex characters and lines of 40.
func objectIDLines(t *testing.T, size int) string {
	t.Helper()
	long, short := strings.Repeat("0", 64)+"\n", strings.Repeat("0", 40)+"\n"
	for n := 0; n < len(long); n++ {
		if rest := size - n*len(short); rest >= 0 && rest%len(long) == 0 {
			return strings.Repeat(long, rest/len(long)) + strings.Repeat(short, n)
		}
	}
	t.Fatalf("no text of object ids has %d bytes", size)
	return ""
}

// TestGitDirFilesSize pins the size bound of info/exclude, info/attributes and
// shallow: 4194304 bytes pass, and 4194305 are refused (cells X6). The text of
// `shallow` has no `: it`.
func TestGitDirFilesSize(t *testing.T) {
	_, gitDir := configRepo(t)
	for name, large := range map[string]string{
		"info/exclude":    ": it is larger than 4 MiB",
		"info/attributes": ": it is larger than 4 MiB",
		"shallow":         " is larger than 4 MiB",
	} {
		path := filepath.Join(gitDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct {
			size    int
			refused bool
		}{{repoGitFileMaxBytes - 1, false}, {repoGitFileMaxBytes, false}, {repoGitFileMaxBytes + 1, true}, {5 << 20, true}} {
			if err := os.WriteFile(path, []byte(objectIDLines(t, c.size)), 0o644); err != nil {
				t.Fatal(err)
			}
			want := ""
			if c.refused {
				want = strconv.Quote(path) + large
			}
			if got := gitDirFilesDetail(gitDir, nil); got != want {
				t.Errorf("%s of %d bytes: detail = %q, want %q", name, c.size, got, want)
			}
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
}

// TestGitDirShallowHoldsObjectIDs pins the test of `shallow` (cells B3 d, E7 and
// X11).
func TestGitDirShallowHoldsObjectIDs(t *testing.T) {
	_, gitDir := configRepo(t)
	path := filepath.Join(gitDir, "shallow")
	id := "98a18a11f5a0168d46f87b8d32db7f5c348a1c49"
	refused := strconv.Quote(path) + " holds something other than object ids"
	for _, c := range []struct{ text, want string }{
		{id + "\n", ""},
		{"", ""},
		{id, ""},
		{id + "\r\n" + id + "\r\n", ""},
		{strings.Repeat("a", 64) + "\n", ""},
		{strings.ToUpper(id) + "\n", refused},
		{id + "\n\n", refused},
		{"\n" + id + "\n", refused},
		// Cells V10: a file that holds one line end and nothing else is refused, LF
		// or CRLF.
		{"\n", refused},
		{"\r\n", refused},
		{id + "\nthis line is not an object id\n", refused},
		{id[:39] + "\n", refused},
	} {
		if err := os.WriteFile(path, []byte(c.text), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := gitDirFilesDetail(gitDir, nil); got != c.want {
			t.Errorf("shallow %q: detail = %q, want %q", c.text, got, c.want)
		}
	}
}

// TestRepoConfigSyntaxError pins the text of a syntax error in a config file: the
// text of git with the path in quotes (cell X10, and cell W2 of
// git.worktree_create on a Windows VM). A file with a NUL byte in
// a value and an empty file are no refusal (cells X10).
func TestRepoConfigSyntaxError(t *testing.T) {
	repo, gitDir := configRepo(t)
	setRepoConfig(t, gitDir, "[core]\n\trepositoryformatversion = 0\n"+baseRepoConfig+"[x\n\tbroken\n = = =\n")
	detail := "fatal: bad config line 6 in " + strconv.Quote(filepath.Join(gitDir, "config"))
	if got := configDetail(t, gitDir); got != detail {
		t.Errorf("syntax error: detail = %q, want %q", got, detail)
	}
	quoted, _ := json.Marshal(repoConfigPrefix + detail)
	want := `"error":{"code":-32603,"message":` + string(quoted) + `}`
	if raw := dispatchRaw(t, newTestServer(t), rpcLine(t, "git.info", map[string]any{"path": repo})); !strings.Contains(raw, want) {
		t.Errorf("git.info with a syntax error = %s, want %s", raw, want)
	}
	// An included file with a syntax error gets the same text, with its own path
	// and its own line number (cells V7, Linux, macOS and Windows VMs).
	setIncludeFile(t, gitDir, "inc.cfg", "[x\n\tbroken\n = = =\n")
	setRepoConfig(t, gitDir, version0+"[include]\n\tpath = inc.cfg\n")
	if got, want := configDetail(t, gitDir), "fatal: bad config line 1 in "+strconv.Quote(filepath.Join(gitDir, "inc.cfg")); got != want {
		t.Errorf("syntax error in an included file: detail = %q, want %q", got, want)
	}
	for name, text := range map[string]string{"a NUL byte": version0 + "[x]\n\tn = a\x00b\n", "an empty file": ""} {
		setRepoConfig(t, gitDir, text)
		if got := configDetail(t, gitDir); got != "" {
			t.Errorf("config with %s: detail = %q, want none", name, got)
		}
	}
	if err := os.Remove(filepath.Join(gitDir, "config")); err != nil {
		t.Fatal(err)
	}
	if got := configDetail(t, gitDir); got != "" {
		t.Errorf("missing config: detail = %q, want none", got)
	}
}

// refusalFixture is a repository with a linked worktree, made before its config
// is changed.
type refusalFixture struct {
	repo, gitDir, wt string
}

func newRefusalFixture(t *testing.T) refusalFixture {
	t.Helper()
	repo, gitDir := configRepo(t)
	wt := filepath.Join(repo, ".claude", "worktrees", "wr")
	runGit(t, repo, "worktree", "add", "-q", wt, "-b", "wr")
	return refusalFixture{repo: repo, gitDir: gitDir, wt: wt}
}

// fiveRequests are one request of each git method on the fixture, with extra
// members on each.
func (f refusalFixture) fiveRequests(extra map[string]any) []struct {
	method string
	params map[string]any
} {
	reqs := []struct {
		method string
		params map[string]any
	}{
		{"git.info", map[string]any{"path": f.repo}},
		{"git.status", map[string]any{"path": f.wt, "baseRepo": f.repo}},
		{"git.list_branches", map[string]any{"path": f.repo}},
		{"git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1",
			"worktreePath": filepath.Join(f.repo, ".claude", "worktrees", "w1")}},
		{"git.worktree_remove", map[string]any{"baseRepo": f.repo, "worktreePath": f.wt}},
	}
	for _, r := range reqs {
		for k, v := range extra {
			r.params[k] = v
		}
	}
	return reqs
}

// noRepositoryAnswers are the answers of the five methods of fiveRequests for a
// folder in which git finds no repository, in that order.
func (f refusalFixture) noRepositoryAnswers() []string {
	lock, _ := json.Marshal("failed to remove worktree: could not check whether " + f.wt +
		" is locked (its registrations could not be examined); retry")
	return []string{
		`"result":{"isRepo":false,"repoSlug":"","defaultBranch":""}`,
		`"result":{"isRepo":false,"clean":false}`,
		`"result":{"isRepo":false,"branches":[]}`,
		`"result":{"success":false,"error":"not a git repository","errorCode":"not_a_repo"}`,
		`"result":{"success":false,"error":` + string(lock) + `}`,
	}
}

// TestRepoConfigRefusalShapes pins the answer of each of the five methods for a
// config that the daemon refuses, and that nothing is made or deleted (cells
// C2, W7, E1 and X1).
func TestRepoConfigRefusalShapes(t *testing.T) {
	f := newRefusalFixture(t)
	setRepoConfig(t, f.gitDir, versionConfig("repositoryformatversion = 2"))
	s := newTestServer(t)
	text := repoConfigPrefix + versionDetail("2")
	quoted, _ := json.Marshal(text)
	wantError := `"error":{"code":-32603,"message":` + string(quoted) + `}`
	removeText, _ := json.Marshal("failed to remove worktree: could not check whether " + f.wt + " is locked: " + text)
	want := []string{wantError, wantError, wantError,
		`"result":{"success":false,"error":` + string(quoted) + `,"errorCode":"worktree_add_failed"}`,
		`"result":{"success":false,"error":` + string(removeText) + `}`}
	for i, m := range f.fiveRequests(nil) {
		if raw := dispatchRaw(t, s, rpcLine(t, m.method, m.params)); !strings.Contains(raw, want[i]) {
			t.Errorf("%s = %s, want %s", m.method, raw, want[i])
		}
	}
	// A linked worktree as the path gets the refusal of the config too (cells X2).
	if raw := dispatchRaw(t, s, rpcLine(t, "git.info", map[string]any{"path": f.wt})); !strings.Contains(raw, wantError) {
		t.Errorf("git.info on the linked worktree = %s, want %s", raw, wantError)
	}
	if leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1"); !missing(leaf) {
		t.Errorf("git.worktree_create made %s, want nothing made", leaf)
	}
	for _, p := range []string{filepath.Join(f.wt, ".git"), filepath.Join(f.gitDir, "worktrees", "wr")} {
		if missing(p) {
			t.Errorf("git.worktree_remove did not keep %s", p)
		}
	}
}

func missing(path string) bool {
	_, err := os.Lstat(path)
	return os.IsNotExist(err)
}

// TestRepoConfigSwitchTurnsTheReadOff pins the `shadowDirOff` member: with the
// JSON value true, format version 2 gets the "not a repository" answers of the
// five methods. Cells X1 measured git.info and git.status, and cells V2 the
// three other methods. The member name in upper case counts as the switch, and
// a text, a number, null and false are as no member (cells V3, on git.info and
// git.status). The cells ran on Linux, macOS and Windows VMs.
func TestRepoConfigSwitchTurnsTheReadOff(t *testing.T) {
	f := newRefusalFixture(t)
	setRepoConfig(t, f.gitDir, versionConfig("repositoryformatversion = 2"))
	s := newTestServer(t)
	want := f.noRepositoryAnswers()
	for _, name := range []string{"shadowDirOff", "SHADOWDIROFF"} {
		for i, m := range f.fiveRequests(map[string]any{name: true}) {
			if raw := dispatchRaw(t, s, rpcLine(t, m.method, m.params)); !strings.Contains(raw, want[i]) {
				t.Errorf("%s with %s true = %s, want %s", m.method, name, raw, want[i])
			}
		}
	}
	for _, v := range []any{"true", 1, nil, false} {
		for _, m := range f.fiveRequests(map[string]any{"shadowDirOff": v}) {
			if raw := dispatchRaw(t, s, rpcLine(t, m.method, m.params)); !strings.Contains(raw, "the repository uses format version") {
				t.Errorf("%s with shadowDirOff %#v = %s, want the version refusal", m.method, v, raw)
			}
		}
	}
}

// TestGitDirFileRefusalShape pins the class of a refused file of the git
// directory on git.info and git.status (cell E7).
func TestGitDirFileRefusalShape(t *testing.T) {
	f := newRefusalFixture(t)
	path := filepath.Join(f.gitDir, "shallow")
	if err := os.WriteFile(path, []byte("this line is not an object id\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t)
	quoted, _ := json.Marshal(repoFilePrefix + strconv.Quote(path) + " holds something other than object ids")
	want := `"error":{"code":-32603,"message":` + string(quoted) + `}`
	for _, m := range f.fiveRequests(nil)[:2] {
		if raw := dispatchRaw(t, s, rpcLine(t, m.method, m.params)); !strings.Contains(raw, want) {
			t.Errorf("%s = %s, want %s", m.method, raw, want)
		}
	}
}

// TestRepoConfigBadHeadKeepsItsAnswers pins the order of result X3: a `HEAD`
// that is no ref keeps the "not a repository" answers of the five methods, also
// with format version 2 (cells E5 and X3).
func TestRepoConfigBadHeadKeepsItsAnswers(t *testing.T) {
	f := newRefusalFixture(t)
	setRepoConfig(t, f.gitDir, versionConfig("repositoryformatversion = 2"))
	if err := os.WriteFile(filepath.Join(f.gitDir, "HEAD"), []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t)
	want := f.noRepositoryAnswers()
	for i, m := range f.fiveRequests(nil) {
		if raw := dispatchRaw(t, s, rpcLine(t, m.method, m.params)); !strings.Contains(raw, want[i]) {
			t.Errorf("%s with a HEAD that is no ref = %s, want %s", m.method, raw, want[i])
		}
	}
}

// TestRepoConfigWorktreeFile pins the tests of a `config.worktree` file (cells
// X2). With extensions.worktreeConfig, the file of a linked worktree that is the
// path of git.info gets the size test, with its own path. Its format version is
// not tested, and the `config.worktree` of the main worktree is not tested.
func TestRepoConfigWorktreeFile(t *testing.T) {
	f := newRefusalFixture(t)
	setRepoConfig(t, f.gitDir, version0+"[extensions]\n\tworktreeConfig = true\n")
	entry := filepath.Join(f.gitDir, "worktrees", "wr")
	setIncludeFile(t, f.gitDir, "config.worktree", refusedInclude)
	setIncludeFile(t, entry, "config.worktree", refusedInclude)
	ctx := context.Background()
	if got := repoConfigDetail(ctx, f.gitDir, entry, nil); got != "" {
		t.Errorf("config.worktree files with format version 2: detail = %q, want none", got)
	}
	big := strings.Repeat("#c\n", (repoConfigMaxBytes+3)/3)
	setIncludeFile(t, f.gitDir, "config.worktree", big)
	if got := repoConfigDetail(ctx, f.gitDir, entry, nil); got != "" {
		t.Errorf("large config.worktree of the main worktree: detail = %q, want none", got)
	}
	setIncludeFile(t, f.gitDir, "config.worktree", "[core]\n\tfilemode = false\n")
	setIncludeFile(t, entry, "config.worktree", big)
	s := newTestServer(t)
	quoted, _ := json.Marshal(repoConfigPrefix + strconv.Quote(filepath.Join(entry, "config.worktree")) + " is larger than 8 MiB")
	want := `"error":{"code":-32603,"message":` + string(quoted) + `}`
	if raw := dispatchRaw(t, s, rpcLine(t, "git.info", map[string]any{"path": f.wt})); !strings.Contains(raw, want) {
		t.Errorf("git.info on the linked worktree = %s, want %s", raw, want)
	}
	if raw := dispatchRaw(t, s, rpcLine(t, "git.info", map[string]any{"path": f.repo})); !strings.Contains(raw, `"result":{"isRepo":true,`) {
		t.Errorf("git.info on the repository = %s, want a repository", raw)
	}
	// The extension starts the test as `yes`, `1`, `TRUE` and with no value too
	// (cells V14, Linux, macOS and Windows VMs), and as `on` and `ON` (cells N4,
	// the same three systems). `false`, `no` and `0` start none (cells N4).
	large := strconv.Quote(filepath.Join(entry, "config.worktree")) + " is larger than 8 MiB"
	for line, want := range map[string]string{
		"worktreeConfig = true": large, "worktreeConfig = yes": large, "worktreeConfig = 1": large,
		"worktreeConfig = TRUE": large, "worktreeConfig": large,
		"worktreeConfig = on": large, "worktreeConfig = ON": large,
		"worktreeConfig = false": "", "worktreeConfig = no": "", "worktreeConfig = 0": "",
	} {
		setRepoConfig(t, f.gitDir, version0+"[extensions]\n\t"+line+"\n")
		if got := repoConfigDetail(ctx, f.gitDir, entry, nil); got != want {
			t.Errorf("extensions.%s, large config.worktree: detail = %q, want %q", line, got, want)
		}
	}
	// Without the extension the file is not tested.
	setRepoConfig(t, f.gitDir, version0)
	if got := repoConfigDetail(ctx, f.gitDir, entry, nil); got != "" {
		t.Errorf("large config.worktree with no extensions.worktreeConfig: detail = %q, want none", got)
	}
}

// TestGitDirFilesOrder pins which file a refusal names when two are refused
// (cells V16). A refused config comes before a file of the git directory
// (Linux, macOS and Windows VMs). `shallow` comes before info/attributes (the
// same three systems), and info/attributes before info/exclude (cells P6, Linux
// VM, four pairs of states).
func TestGitDirFilesOrder(t *testing.T) {
	_, gitDir := configRepo(t)
	big := objectIDLines(t, repoGitFileMaxBytes+1)
	for _, name := range []string{"attributes", "exclude"} {
		writeFile(t, filepath.Join(gitDir, "info", name), big, 0o644)
	}
	if got, want := gitDirFilesDetail(gitDir, nil), strconv.Quote(filepath.Join(gitDir, "info", "attributes"))+": it is larger than 4 MiB"; got != want {
		t.Errorf("two large info files: detail = %q, want %q", got, want)
	}
	shallow := filepath.Join(gitDir, "shallow")
	writeFile(t, shallow, "this line is not an object id\n", 0o644)
	if got, want := gitDirFilesDetail(gitDir, nil), strconv.Quote(shallow)+" holds something other than object ids"; got != want {
		t.Errorf("a refused shallow and two large info files: detail = %q, want %q", got, want)
	}
	setRepoConfig(t, gitDir, versionConfig("repositoryformatversion = 2"))
	v := repoReadVerdict(gitDirTrust{verdict: gitDirTrusted, pinCommonDir: gitDir})
	if want := repoConfigPrefix + versionDetail("2"); v == nil || v.refusal != want {
		t.Errorf("a refused config and three refused files: verdict = %+v, want %q", v, want)
	}
}

// TestGitDirFilesOnAFile pins the refusal of a git directory that is a regular file:
// the error of its open as a folder, with no quotes. A `.git` file of a linked
// worktree gets it with the class 2 prefix (cells c19 to c22 of round task039-01, and
// V-S3 and V-RM of round task039-02, Windows VM). A file with no `gitdir:` line gets it
// with the config prefix, from the read of the config (cell V-Wd of round
// task039-02, Windows VM). The tests run on every system. From the code: on Linux
// and macOS the read of the config refuses a `.git` file first, and the trust check
// sends no such git directory there. Mutations: drop the test in gitDirFilesDetail, drop the test in
// readRepoConfig, or apply it to a `.git` file too.
func TestGitDirFilesOnAFile(t *testing.T) {
	dir := t.TempDir()
	gitFile := filepath.Join(dir, ".git")
	writeFile(t, gitFile, "gitdir: elsewhere\n", 0o644)
	fileWant := "a file in this repository's .git directory cannot be used: open " + gitFile + ": not a directory"
	if got := gitDirFilesRefusal(gitFile); got != fileWant {
		t.Errorf("files refusal = %q\nwant %q", got, fileWant)
	}
	plain := filepath.Join(dir, "gitdir-file")
	writeFile(t, plain, "x", 0o644)
	plainWant := "this repository's git config cannot be used: open " + plain + ": not a directory"
	if msg, _ := repoConfigGate(gitDirTrust{verdict: gitDirTrusted, pinCommonDir: plain}, false); msg != plainWant {
		t.Errorf("plain file: refusal = %q\nwant %q", msg, plainWant)
	}
	msg, _ := repoConfigGate(gitDirTrust{verdict: gitDirTrusted, pinCommonDir: gitFile}, false)
	if runtime.GOOS == "windows" && msg != fileWant {
		t.Errorf(".git file: refusal = %q\nwant %q", msg, fileWant)
	}
	if msg == "this repository's git config cannot be used: open "+gitFile+": not a directory" {
		t.Errorf(".git file: refusal = %q, the text of a file with no gitdir line", msg)
	}
}

// TestRepoConfigLongPathIsCut pins the cut of a path in quotes: its first 300
// characters stay, with no mark of the cut (cells X12). A path of 300
// characters is whole, and the cut counts characters, not bytes.
func TestRepoConfigLongPathIsCut(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	shallowAt := func(gitDir string) string {
		t.Helper()
		if err := os.MkdirAll(gitDir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(gitDir, "shallow")
		if err := os.WriteFile(path, []byte("no object id\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	// folder makes a folder below root whose file `shallow` has a path of n
	// characters. Each folder name has at most 100 characters.
	folder := func(n int, fill string) string {
		dir := root
		for extra := n - utf8.RuneCountInString(root) - len("/shallow"); extra > 0; {
			take := min(extra, 101)
			if extra-take == 1 {
				take--
			}
			dir = filepath.Join(dir, strings.Repeat(fill, take-1))
			extra -= take
		}
		return dir
	}
	const tail = " holds something other than object ids"
	for _, c := range []struct {
		chars int
		fill  string
	}{{300, "p"}, {301, "p"}, {400, "p"}, {300, "é"}, {301, "é"}} {
		gitDir := folder(c.chars, c.fill)
		path := shallowAt(gitDir)
		if n := utf8.RuneCountInString(path); n != c.chars {
			t.Fatalf("fixture path has %d characters, want %d", n, c.chars)
		}
		want := strconv.Quote(string([]rune(path)[:300])) + tail
		if got := gitDirFilesDetail(gitDir, nil); got != want {
			t.Errorf("path of %d characters of %s: detail = %q, want %q", c.chars, c.fill, got, want)
		}
	}
}

// forgetRepoVerdicts drops each kept verdict of the config read, so that the
// next request on a repository reads its config.
func forgetRepoVerdicts() {
	repoVerdicts.Lock()
	defer repoVerdicts.Unlock()
	repoVerdicts.byKey, repoVerdicts.order = nil, nil
}

// stubGitWithLog puts the git stub of the test binary on PATH and returns the
// file that gets the argv of each git call.
func stubGitWithLog(t *testing.T) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	installGitSlowStub(t, realGit)
	log := filepath.Join(t.TempDir(), "argv.log")
	writeFile(t, log, "", 0o644)
	t.Setenv("CLAUSTRUM_GITSTUB_LOG", log)
	return log
}

// TestRepoVerdictIsKept pins that a second request on a repository starts no git
// call for the read of the config, and that a changed file is read again. The
// first two steps are git.info requests, and the others run the read alone.
// With an accepted config, 5fd08069 makes no such call for a second git.info,
// and one after a change of the config (cell B4, Linux VM). With a refused
// format version it makes the read again at each request (cells N10 and P5,
// Linux, macOS and Windows VMs), and claustrum does too. The rows for an included file are claustrum's
// own: a changed one is read again, and so is one that was missing and is there
// now.
func TestRepoVerdictIsKept(t *testing.T) {
	repo, gitDir := configRepo(t)
	log := stubGitWithLog(t)
	s := newTestServer(t)
	reads := 0
	grew := func(name string, want int) {
		t.Helper()
		now := len(gitCalls(t, log, "--no-includes"))
		if now-reads != want {
			t.Errorf("%s: %d read calls, want %d", name, now-reads, want)
		}
		reads = now
	}
	for _, c := range []struct {
		name string
		want int
	}{{"first git.info", 1}, {"second git.info", 0}} {
		if raw := dispatchRaw(t, s, rpcLine(t, "git.info", map[string]any{"path": repo})); !strings.Contains(raw, `"result":{"isRepo":true,`) {
			t.Errorf("%s = %s, want a repository", c.name, raw)
		}
		grew(c.name, c.want)
	}
	// The next steps run the read of the gate alone, so that each step starts no
	// other git call. step fails unless the count of read calls grew by want.
	trust := gitDirTrust{verdict: gitDirTrusted, pinCommonDir: gitDir}
	// refusal is a part of the refusal text, or "" for no refusal.
	step := func(name string, want int, refusal string) {
		t.Helper()
		got := ""
		if v := repoReadVerdict(trust); v != nil {
			if got = v.refusal; got == "" {
				got = gitDirFilesRefusal(trust.pinCommonDir)
			}
		}
		if !strings.Contains(got, refusal) || (refusal == "" && got != "") {
			t.Errorf("%s: verdict = %q, want %q", name, got, refusal)
		}
		grew(name, want)
	}
	step("third request", 0, "")

	setRepoConfig(t, gitDir, version0+"[x]\n\ty = 1\n")
	step("changed config", 1, "")
	step("changed config, second request", 0, "")

	setIncludeFile(t, gitDir, "inc.cfg", "[x]\n\ta = 1\n")
	setRepoConfig(t, gitDir, version0+"[include]\n\tpath = inc.cfg\n\tpath = late.cfg\n")
	step("config with an included file and a missing one", 2, "")
	step("the same, second request", 0, "")

	setIncludeFile(t, gitDir, "inc.cfg", "[x]\n\ta = 1\n\tb = 22\n")
	step("changed included file", 2, "")
	step("changed included file, second request", 0, "")

	setIncludeFile(t, gitDir, "late.cfg", refusedInclude+"[include]\n\tpath = %(prefix)/x\n")
	step("a file where an include was missing", 3, `"%(prefix)/x", and Claude expands neither`)
	step("a refusal, second request", 3, `"%(prefix)/x", and Claude expands neither`)

	// A file of the git directory is no part of the read: its test runs in each
	// request, and a change of it leads to no new read of the config (cells 19a
	// to 19c of val-s3p2b2-3-linux, Linux VM).
	setRepoConfig(t, gitDir, version0)
	step("config with no include", 1, "")
	writeFile(t, filepath.Join(gitDir, "shallow"), "this line is not an object id\n", 0o644)
	step("a shallow file that appears", 0, "holds something other than object ids")
	step("a refused shallow file, second request", 0, "holds something other than object ids")
}

// TestWorktreeConfigListingsBeforeAnExtensionRefusal pins the count of the
// listings of the read with a good `config.worktree` file and the worktreeConfig
// switch. With a refused extension under format version 1, 5fd08069 makes two
// listings (cell Q11 of tier0-18, Linux VM). With no refusal it makes three
// (cell C3c of tier0-14, Linux VM).
func TestWorktreeConfigListingsBeforeAnExtensionRefusal(t *testing.T) {
	_, gitDir := configRepo(t)
	setIncludeFile(t, gitDir, "config.worktree", "[core]\n\tquotepath = off\n")
	log := stubGitWithLog(t)
	listings := 0
	for _, row := range []struct {
		extension, detail string
		listings          int
	}{
		{"\tpreciousObjects = 1234567890\n", extensionDetail("preciousobjects"), 2},
		{"", "", 3},
	} {
		setRepoConfig(t, gitDir, versionConfig("repositoryformatversion = 1")+"[extensions]\n\tworktreeConfig = true\n"+row.extension)
		if _, got := readRepoConfig(context.Background(), gitDir, nil); got != row.detail {
			t.Errorf("extension %q: detail = %q, want %q", row.extension, got, row.detail)
		}
		now := len(gitCalls(t, log, "--no-includes"))
		if now-listings != row.listings {
			t.Errorf("extension %q: %d listings, want %d", row.extension, now-listings, row.listings)
		}
		listings = now
	}
}

// TestRepoVerdictsBound pins the bound of the kept verdicts: the oldest one goes
// when one more than repoVerdictsMax is kept. Many requests at once are safe.
func TestRepoVerdictsBound(t *testing.T) {
	repoVerdicts.Lock()
	savedKeys, savedOrder := repoVerdicts.byKey, repoVerdicts.order
	repoVerdicts.byKey, repoVerdicts.order = nil, nil
	repoVerdicts.Unlock()
	t.Cleanup(func() {
		repoVerdicts.Lock()
		repoVerdicts.byKey, repoVerdicts.order = savedKeys, savedOrder
		repoVerdicts.Unlock()
	})
	key := func(i int) repoVerdictKey { return repoVerdictKey{gitDir: "repo-" + strconv.Itoa(i)} }
	var wg sync.WaitGroup
	for i := range repoVerdictsMax {
		wg.Add(1)
		go func() {
			defer wg.Done()
			keepRepoVerdict(key(i), &repoVerdict{refusal: strconv.Itoa(i)})
			_, _ = keptRepoVerdict(key(i))
		}()
	}
	wg.Wait()
	repoVerdicts.Lock()
	oldest := repoVerdicts.order[0]
	repoVerdicts.Unlock()
	// A verdict that is kept again keeps its place and adds no entry.
	keepRepoVerdict(oldest, &repoVerdict{refusal: "again"})
	if got, ok := keptRepoVerdict(oldest); !ok || got.refusal != "again" {
		t.Errorf("verdict kept again = %+v, %v, want again", got, ok)
	}
	keepRepoVerdict(key(repoVerdictsMax), &repoVerdict{})
	if _, ok := keptRepoVerdict(oldest); ok {
		t.Errorf("the oldest verdict is still kept after %d more", repoVerdictsMax)
	}
	if _, ok := keptRepoVerdict(key(repoVerdictsMax)); !ok {
		t.Error("the newest verdict is not kept")
	}
	repoVerdicts.Lock()
	n, m := len(repoVerdicts.byKey), len(repoVerdicts.order)
	repoVerdicts.Unlock()
	if n != repoVerdictsMax || m != repoVerdictsMax {
		t.Errorf("kept verdicts = %d, order = %d, want %d each", n, m, repoVerdictsMax)
	}
}

// TestRepoConfigRefusalOnAGoneWorktree pins git.worktree_remove for a worktree
// folder that does not exist, in a repository that the daemon refuses. The
// answer is the refusal of a folder that exists, and the branch stays. 5fd08069
// answers so on a Windows VM, where 89cb6289 answers success. That build deletes
// the branch with the files of 20 MiB and with the absolute include (cells G1a to
// G1c), and keeps it with format version `2` (cells F1a, F2a and W6x). Without a
// refused file the remove answers success and deletes the branch (cell G1z).
func TestRepoConfigRefusalOnAGoneWorktree(t *testing.T) {
	f := newRefusalFixture(t)
	runGit(t, f.repo, "branch", "spare")
	ref := filepath.Join(f.gitDir, "refs", "heads", "spare")
	gone := filepath.Join(f.repo, ".claude", "worktrees", "nope")
	s := newTestServer(t)
	remove := func() string {
		return dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
			map[string]any{"baseRepo": f.repo, "worktreePath": gone, "branchName": "spare"}))
	}
	exclude := filepath.Join(f.gitDir, "info", "exclude")
	writeFile(t, exclude, objectIDLines(t, repoGitFileMaxBytes+1), 0o644)
	text, _ := json.Marshal("failed to remove worktree: could not check whether " + gone + " is locked: " +
		repoFilePrefix + strconv.Quote(exclude) + ": it is larger than 4 MiB")
	if raw, want := remove(), `"result":{"success":false,"error":`+string(text)+`}`; !strings.Contains(raw, want) {
		t.Errorf("remove of a gone worktree in a refused repository = %s, want %s", raw, want)
	}
	if missing(ref) {
		t.Error("the refused remove deleted the branch")
	}

	writeFile(t, exclude, "", 0o644)
	if raw, want := remove(), `"result":{"success":true}`; !strings.Contains(raw, want) {
		t.Errorf("remove of a gone worktree = %s, want %s", raw, want)
	}
	if !missing(ref) {
		t.Error("the remove kept the branch, want it deleted")
	}
}

// TestRepoVerdictNotKeptAfterAnOpenError pins that the verdict of a read is not
// kept when an open of the daemon failed in it. The open of the test fails one
// time with the text of a full file table, an error of the daemon itself. The
// request of that read gets its verdict, and the next request reads again. The
// rule is claustrum's own: no cell has such an error.
//
//   - The config and `shallow`: the first verdict is the refusal with the error
//     text, and the second is no refusal.
//   - A large file of an `includeIf` section: the first verdict is no refusal,
//     and the second is the size refusal.
func TestRepoVerdictNotKeptAfterAnOpenError(t *testing.T) {
	_, gitDir := configRepo(t)
	trust := gitDirTrust{verdict: gitDirTrusted, pinCommonDir: gitDir}
	t.Cleanup(func() { repoFileOpen = os.OpenFile })
	// failOnce makes the next open of path fail.
	failOnce := func(path string) {
		failed := false
		repoFileOpen = func(name string, flag int, perm os.FileMode) (*os.File, error) {
			if name == path && !failed {
				failed = true
				return nil, &fs.PathError{Op: "open", Path: name, Err: errors.New("too many open files")}
			}
			return os.OpenFile(name, flag, perm)
		}
	}
	verdict := func() string {
		t.Helper()
		v := repoReadVerdict(trust)
		if v == nil {
			t.Fatal("the read gave no verdict")
		}
		if v.refusal != "" {
			return v.refusal
		}
		return gitDirFilesRefusal(trust.pinCommonDir)
	}
	config, shallow, large := filepath.Join(gitDir, "config"), filepath.Join(gitDir, "shallow"), filepath.Join(gitDir, "im.cfg")
	writeFile(t, shallow, strings.Repeat("a", 40)+"\n", 0o644)
	writeFile(t, large, strings.Repeat("#c\n", (repoConfigMaxBytes+3)/3), 0o644)
	for _, c := range []struct{ name, path, config, first, second string }{
		{"the config", config, version0, repoConfigPrefix + "open " + config + ": too many open files", ""},
		{"shallow", shallow, version0, repoFilePrefix + "open " + shallow + ": too many open files", ""},
		{"the file of an includeIf section", large, version0 + "[includeIf \"onbranch:main\"]\n\tpath = im.cfg\n",
			"", repoConfigPrefix + strconv.Quote(large) + " is larger than 8 MiB"},
	} {
		setRepoConfig(t, gitDir, c.config)
		forgetRepoVerdicts()
		failOnce(c.path)
		if got := verdict(); got != c.first {
			t.Errorf("%s, the open fails: verdict = %q, want %q", c.name, got, c.first)
		}
		if got := verdict(); got != c.second {
			t.Errorf("%s, the next request: verdict = %q, want %q", c.name, got, c.second)
		}
	}
}

// TestRepoConfigListingIsBound pins that the read takes no listing over
// repoListingMaxBytes into memory. A config with a long subsection name and many
// keys lists as far more bytes than the file has. With the bound the read has no
// verdict, so the refused format version of the same file gives no refusal here.
// Without the bound the read lists the whole file and answers that refusal. The
// control has the same file with a short subsection name. The same file in the
// section `remote` keeps its name bound refusal, because the kept part of the
// listing shows it. Not measured on 5fd08069.
func TestRepoConfigListingIsBound(t *testing.T) {
	requireGit(t)
	old := repoListingMaxBytes
	t.Cleanup(func() { repoListingMaxBytes = old })
	repoListingMaxBytes = 1 << 20
	config := func(subsection string) string { return listingConfig("x", subsection) }
	_, gitDir := configRepo(t)
	setRepoConfig(t, gitDir, config(strings.Repeat("s", 8<<10)))
	log := &repoReadLog{}
	if d := repoConfigDetail(context.Background(), gitDir, "", log); d != "" || !log.noVerdict {
		t.Errorf("a listing over the bound: detail %q, no verdict %v, want no detail and no verdict", d, log.noVerdict)
	}
	setRepoConfig(t, gitDir, config("s"))
	log = &repoReadLog{}
	if d := repoConfigDetail(context.Background(), gitDir, "", log); !strings.Contains(d, "format version") || log.noVerdict {
		t.Errorf("the control: detail %q, no verdict %v, want the format version refusal", d, log.noVerdict)
	}
	setRepoConfig(t, gitDir, listingConfig("remote", strings.Repeat("s", 8<<10)))
	log = &repoReadLog{}
	if d := repoConfigDetail(context.Background(), gitDir, "", log); !strings.Contains(d, "whose name is longer than") || log.noVerdict {
		t.Errorf("a listing over the bound in a section with a name bound: detail %q, no verdict %v, want the name refusal", d, log.noVerdict)
	}
}

// listingConfig is a config of format version 2 with 400 keys in one subsection.
func listingConfig(section, subsection string) string {
	var b strings.Builder
	b.WriteString(versionConfig("repositoryformatversion = 2"))
	b.WriteString("[" + section + " \"" + subsection + "\"]\n")
	for i := range 400 {
		b.WriteString("\tk" + strconv.Itoa(i) + " = 1\n")
	}
	return b.String()
}
