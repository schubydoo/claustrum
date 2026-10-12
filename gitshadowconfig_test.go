package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// shadowConfigCell is one cell of testdata/shadow_config_cells.json: the files
// of the fixture, by their path below the git directory, and the text of the
// copy that 5fd08069 wrote on a Linux VM with git 2.43.0. The file is made from
// the records of the rounds s3p2-linux, s3p2b-edges-linux and
// s3p2b-edges2-linux. Two things differ from the records. The path of the VM
// work folder is written /vm/w. For cells 8a to 8g the records do not hold the
// included files: they come from the report of the round.
type shadowConfigCell struct {
	Round string            `json:"round"`
	Cell  string            `json:"cell"`
	Files map[string]string `json:"files"`
	Want  string            `json:"want"`
}

// copyTextOf writes the files of a cell below a new git directory and returns
// the text of the copy for it, through the read of the daemon. {HOME} in a
// path is home, and {OUT} is a folder outside the repository.
func copyTextOf(t *testing.T, files map[string]string, home string) (string, error) {
	base := resolveTestRoot(t, t.TempDir())
	gitDir := filepath.Join(base, "repo", ".git")
	place := strings.NewReplacer("{HOME}", home, "{OUT}", base)
	for name, text := range files {
		path := place.Replace(name)
		if !filepath.IsAbs(path) {
			path = filepath.Join(gitDir, filepath.FromSlash(path))
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte(place.Replace(text)), 0o644); err != nil {
			return "", err
		}
	}
	r, detail := readRepoConfig(context.Background(), gitDir, nil)
	if detail != "" || r.unlisted {
		t.Errorf("the read refused the fixture: %q (no listing: %v)", detail, r.unlisted)
	}
	return shadowConfigText(r.settings, r.worktree), nil
}

// TestShadowConfigCells runs every cell of the table: the copy of the settings
// has the text that 5fd08069 wrote for the same files.
func TestShadowConfigCells(t *testing.T) {
	requireGit(t)
	b, err := os.ReadFile(filepath.Join("testdata", "shadow_config_cells.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cells []shadowConfigCell
	if err := json.Unmarshal(b, &cells); err != nil {
		t.Fatal(err)
	}
	if len(cells) < 200 {
		t.Fatalf("the table has %d cells", len(cells))
	}
	home := resolveTestRoot(t, t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	var wg sync.WaitGroup
	slots := make(chan struct{}, 8)
	for _, c := range cells {
		// On Windows claustrum refuses an include with an absolute path or with
		// `~/` (the rule of the config refusals), so these two Linux cells have
		// no text there (val-s3p2b2-3-windows part C).
		if runtime.GOOS == "windows" && (c.Cell == "8a-absolute-path-outside-the-git-folder" || c.Cell == "8e-tilde-path") {
			continue
		}
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			got, err := copyTextOf(t, c.Files, home)
			if err != nil {
				t.Errorf("%s cell %s: %v", c.Round, c.Cell, err)
				return
			}
			if got != c.Want {
				t.Errorf("%s cell %s:\n got %q\nwant %q", c.Round, c.Cell, got, c.Want)
			}
		}()
	}
	wg.Wait()
}

const (
	stdCopyBlocks = "[core]\n\trepositoryformatversion = \"0\"\n[core]\n\tfilemode = \"true\"\n" +
		"[core]\n\tbare = \"false\"\n[core]\n\tlogallrefupdates = \"true\"\n"
	stdRepoConfig = "[core]\n\trepositoryformatversion = 0\n" + baseRepoConfig
)

// TestShadowConfigOtherSystems has the rows whose text is not in the Linux
// table: the copy of a fresh repository on macOS and on Windows, and the byte
// 0xff, which a JSON file cannot hold.
func TestShadowConfigOtherSystems(t *testing.T) {
	requireGit(t)
	rows := []struct {
		cell, config, want string
	}{
		{
			// s3-macos cell A3 and cell 24 of s3p2b-edges on a macOS VM: 219 bytes.
			cell: "fresh repository, macOS",
			config: "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = false\n" +
				"\tlogallrefupdates = true\n\tignorecase = true\n\tprecomposeunicode = true\n",
			want: stdCopyBlocks + "[core]\n\tignorecase = \"true\"\n[core]\n\tprecomposeunicode = \"true\"\n" + shadowConfigTail,
		},
		{
			// s3-windows cell A3 and cell 24 of s3p2b-edges on a Windows VM: 212 bytes.
			cell: "fresh repository, Windows",
			config: "[core]\n\trepositoryformatversion = 0\n\tfilemode = false\n\tbare = false\n" +
				"\tlogallrefupdates = true\n\tsymlinks = false\n\tignorecase = true\n",
			want: "[core]\n\trepositoryformatversion = \"0\"\n[core]\n\tfilemode = \"false\"\n[core]\n\tbare = \"false\"\n" +
				"[core]\n\tlogallrefupdates = \"true\"\n[core]\n\tsymlinks = \"false\"\n[core]\n\tignorecase = \"true\"\n" + shadowConfigTail,
		},
		{
			// Cell F1 of s3p2b-edges-linux, remote u04: the byte is carried as it is.
			cell:   "F1 byte 0xff in a URL",
			config: stdRepoConfig + "[remote \"u04-byte-ff\"]\n\turl = \"https://example.invalid/o/a\xffb.git\"\n",
			want:   stdCopyBlocks + "[remote \"u04-byte-ff\"]\n\turl = \"https://example.invalid/o/a\xffb.git\"\n" + shadowConfigTail,
		},
	}
	for _, row := range rows {
		got, err := copyTextOf(t, map[string]string{"config": row.config}, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if got != row.want {
			t.Errorf("%s:\n got %q\nwant %q", row.cell, got, row.want)
		}
	}
	if n := len(stdCopyBlocks + shadowConfigTail); n != 156 {
		t.Errorf("the copy of a fresh Linux repository has %d bytes, want 156", n)
	}
}

// TestShadowConfigLeavesOutWhatNoCellShows pins claustrum's choices for inputs
// that no cell ran. Each one is left out of the copy. These rows are not parity
// rows.
func TestShadowConfigLeavesOutWhatNoCellShows(t *testing.T) {
	set := func(key, value string) repoSetting {
		return repoSetting{key: key, value: value, hasValue: true}
	}
	included := func(key, value string) repoSetting {
		return repoSetting{key: key, value: value, hasValue: true, included: true}
	}
	rule := set("url.https://example.invalid/.insteadof", "gh:")
	rows := []struct {
		name     string
		settings []repoSetting
		want     string
	}{
		{"a value of core.eol that no cell ran", []repoSetting{set("core.eol", "crlf")}, ""},
		{"a value of index.version that no cell ran", []repoSetting{set("index.version", "2")}, ""},
		{"a value of core.sharedrepository that git refuses", []repoSetting{set("core.sharedrepository", "0440")}, ""},
		{"core.sharedrepository in another letter case", []repoSetting{set("core.sharedrepository", "GROUP")}, ""},
		{"core.sharedrepository as a number", []repoSetting{set("core.sharedrepository", "1")}, ""},
		{"core.sharedrepository with no value", []repoSetting{{key: "core.sharedrepository"}}, ""},
		{"a boolean with a sign", []repoSetting{set("core.filemode", "-1")}, ""},
		{"a boolean with a unit letter", []repoSetting{set("core.filemode", "1k")}, ""},
		{"another filter of a partial clone", []repoSetting{set("remote.origin.partialclonefilter", "tree:0")}, ""},
		{"a URL with another scheme", []repoSetting{set("remote.origin.url", "HTTPS://example.invalid/o/r.git")}, ""},
		{"a URL with a host of one letter", []repoSetting{set("remote.origin.url", "h:o/r.git")}, ""},
		{"a URL with a dash after the scheme", []repoSetting{set("remote.origin.url", "ssh://-x/o/r.git")}, ""},
		{"a URL with a control byte that no cell ran", []repoSetting{set("remote.origin.url", "https://example.invalid/o/a\x02b.git")}, ""},
		{"a remote name that starts with a digit", []repoSetting{set("remote.9x.url", "https://example.invalid/o/r.git")}, ""},
		{"a remote name with two dots in a row", []repoSetting{set("remote.a..b.url", "https://example.invalid/o/r.git")}, ""},
		{"a branch name with a control character", []repoSetting{set("branch.a\x01b.remote", "origin")}, ""},
		{"a branch name with a character of no cell", []repoSetting{set("branch.a)b.remote", "origin")}, ""},
		{"an lfs pattern with a comma", []repoSetting{set("lfs.fetchinclude", "*.bin,*.zip")}, ""},
		{"an lfs URL with another scheme", []repoSetting{set("lfs.url", "ssh://example.invalid/o/r")}, ""},
		{"an extension of an included file", []repoSetting{included("extensions.noop", "true")}, ""},
		{"a key of a section with no cell", []repoSetting{set("lfs.repositoryformatversion", "0")}, ""},
		{"a remote key with no subsection", []repoSetting{set("remote.url", "https://example.invalid/o/r.git")}, ""},
		{
			"a rule of an included file is not applied",
			[]repoSetting{included(rule.key, rule.value), set("remote.origin.url", "gh:o/r.git")},
			"[remote \"origin\"]\n\turl = \"gh:o/r.git\"\n",
		},
		{
			"a resolved URL of a form that is left out",
			[]repoSetting{set("url.file:///x/.insteadof", "gh:"), set("remote.origin.url", "gh:o/r.git")},
			"",
		},
		{
			"the url blocks stand before a block of another section",
			[]repoSetting{rule, set("remote.origin.url", "gh:o/r.git"), set("branch.main.remote", "origin")},
			"[remote \"origin\"]\n\turl = \"https://example.invalid/o/r.git\"\n" +
				"[url \"https://example.invalid/o/r.git\"]\n\tinsteadOf = \"https://example.invalid/o/r.git\"\n" +
				"[branch \"main\"]\n\tremote = \"origin\"\n",
		},
		{
			"of two rules of one length the first wins",
			[]repoSetting{set("url.https://a.invalid/.insteadof", "gh:"), set("url.https://b.invalid/.insteadof", "gh:"), set("remote.origin.url", "gh:o")},
			"[remote \"origin\"]\n\turl = \"https://a.invalid/o\"\n[url \"https://a.invalid/o\"]\n\tinsteadOf = \"https://a.invalid/o\"\n",
		},
	}
	for _, row := range rows {
		if got := shadowConfigText(row.settings, nil); got != row.want+shadowConfigTail {
			t.Errorf("%s:\n got %q\nwant %q", row.name, got, row.want+shadowConfigTail)
		}
	}
	// A key of `config.worktree` other than core.bare, and a core.bare there
	// with a value that is no boolean.
	worktree := []repoSetting{set("core.filemode", "false"), set("core.bare", "maybe"), set("core.bare", "yes")}
	if got, want := shadowConfigText(nil, worktree), "[core]\n\tbare = \"true\"\n"+shadowConfigTail; got != want {
		t.Errorf("config.worktree:\n got %q\nwant %q", got, want)
	}
}

// TestSharedRepositoryValues pins the reading of each `core.sharedRepository`
// value: the line of the copy, and the mode of the folder `worktrees` after a
// mkdir with a daemon umask of 022 and of 002. The measured rows are the 18
// rows of group P5 of val-s3p2b2-6-linux (Linux VM): section 0.2 item 6 of its
// report has the table. The last rows have no cell. They follow the rule of the
// git documentation for the key, or they give nothing.
func TestSharedRepositoryValues(t *testing.T) {
	const none = os.FileMode(0)
	for _, row := range []struct {
		value        string
		line         string
		u022, u002   os.FileMode
		notMeasured  bool
		withoutValue bool
	}{
		{value: "group", line: "group", u022: 0o2775, u002: 0o2775},
		{value: "true", line: "group", u022: 0o2775, u002: 0o2775},
		{value: "all", line: "everybody", u022: 0o2775, u002: 0o2775},
		{value: "world", line: "everybody", u022: 0o2775, u002: 0o2775},
		{value: "everybody", line: "everybody", u022: 0o2775, u002: 0o2775},
		{value: "0660", line: "0660", u022: 0o2770, u002: 0o2770},
		{value: "0640", line: "0640", u022: 0o2750, u002: 0o2750},
		{value: "umask", line: "umask", u022: none, u002: none},
		{value: "false", line: "umask", u022: none, u002: none, notMeasured: true},
		{value: "0600", line: "0600", u022: 0o2700, u002: 0o2700, notMeasured: true},
		{value: "0664", line: "0664", u022: 0o2775, u002: 0o2775, notMeasured: true},
		{value: "0440", notMeasured: true},
		{value: "660", notMeasured: true},
		{value: "0668", notMeasured: true},
		{value: "Group", notMeasured: true},
		{value: "yes", notMeasured: true},
		{value: "2", notMeasured: true},
		{value: "", notMeasured: true},
		{withoutValue: true, notMeasured: true},
	} {
		name := row.value
		if row.notMeasured {
			name += " (not measured)"
		}
		t.Run(name, func(t *testing.T) {
			s := repoSetting{key: "core.sharedrepository", value: row.value, hasValue: !row.withoutValue}
			r := sharedRepositoryOf(s)
			if r.word != row.line {
				t.Errorf("the value of the line = %q, want %q", r.word, row.line)
			}
			want := shadowConfigTail
			if row.line != "" {
				want = "[core]\n\tsharedrepository = \"" + row.line + "\"\n" + shadowConfigTail
			}
			if got := shadowConfigText([]repoSetting{s}, nil); got != want {
				t.Errorf("the copy = %q, want %q", got, want)
			}
			for _, m := range []struct {
				made, want os.FileMode
			}{{0o755, row.u022}, {0o775, row.u002}} {
				mode, ok := r.folderMode(os.ModeDir | m.made)
				got := mode.Perm()
				if mode&os.ModeSetgid != 0 {
					got |= 0o2000
				}
				if ok != (m.want != none) || got != m.want {
					t.Errorf("after a mkdir of %o: mode %o, %v, want %o", m.made, got, ok, m.want)
				}
			}
		})
	}
}
