package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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
//
// The cells of round tier0-14-linux have the copy texts, the `-c` items and
// the refusal texts of the report of that round. The files are the inputs of
// the round. Cell E3 of round tier0-16-linux has its refusal text. Items are the `-c` items of the `config.worktree`
// file, tested in these cells only. Refusal is the message of the refusal, with {GITDIR} for the git
// directory. A cell with a refusal has no copy.
type shadowConfigCell struct {
	Round   string            `json:"round"`
	Cell    string            `json:"cell"`
	Files   map[string]string `json:"files"`
	Want    string            `json:"want"`
	Items   []string          `json:"items,omitempty"`
	Refusal string            `json:"refusal,omitempty"`
}

// readCellFiles writes the files of a cell below a new git directory and
// returns the read of the daemon for it. {HOME} in a path is home, and {OUT}
// is a folder outside the repository.
func readCellFiles(t *testing.T, files map[string]string, home string) (r *repoConfigRead, detail, gitDir string, err error) {
	base := resolveTestRoot(t, t.TempDir())
	gitDir = filepath.Join(base, "repo", ".git")
	place := strings.NewReplacer("{HOME}", home, "{OUT}", base)
	for name, text := range files {
		path := place.Replace(name)
		if !filepath.IsAbs(path) {
			path = filepath.Join(gitDir, filepath.FromSlash(path))
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, "", "", err
		}
		if err := os.WriteFile(path, []byte(place.Replace(text)), 0o644); err != nil {
			return nil, "", "", err
		}
	}
	r, detail = readRepoConfig(context.Background(), gitDir, nil)
	return r, detail, gitDir, nil
}

// copyTextOf is the text of the copy for the files of a cell, through the read
// of the daemon.
func copyTextOf(t *testing.T, files map[string]string, home string) (string, error) {
	r, detail, _, err := readCellFiles(t, files, home)
	if err != nil {
		return "", err
	}
	if detail != "" || r.unlisted {
		t.Errorf("the read refused the fixture: %q (no listing: %v)", detail, r.unlisted)
	}
	return shadowConfigText(r.settings, r.worktree), nil
}

// TestShadowConfigCells runs every cell of the table: the copy of the settings
// has the text that 5fd08069 wrote for the same files. A cell with items or a
// refusal tests those too.
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
			r, detail, gitDir, err := readCellFiles(t, c.Files, home)
			if err != nil {
				t.Errorf("%s cell %s: %v", c.Round, c.Cell, err)
				return
			}
			if c.Refusal != "" {
				want := strings.ReplaceAll(c.Refusal, `"{GITDIR}/config.worktree"`, quoteOperand(filepath.Join(gitDir, "config.worktree")))
				if got := repoConfigPrefix + detail; detail == "" || got != want {
					t.Errorf("%s cell %s: refusal\n got %q\nwant %q", c.Round, c.Cell, got, want)
				}
				return
			}
			if detail != "" || r.unlisted {
				t.Errorf("%s cell %s: the read refused the fixture: %q (no listing: %v)", c.Round, c.Cell, detail, r.unlisted)
				return
			}
			if got := shadowConfigText(r.settings, r.worktree); got != c.Want {
				t.Errorf("%s cell %s:\n got %q\nwant %q", c.Round, c.Cell, got, c.Want)
			}
			// The older rounds hold no items.
			if got := worktreeConfigItems(r.worktree); c.Round == "tier0-14-linux" && !slices.Equal(got, c.Items) {
				t.Errorf("%s cell %s: items %q, want %q", c.Round, c.Cell, got, c.Items)
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

// TestShadowConfigNotMeasured pins claustrum's choices for inputs that no cell
// ran. Each one follows copyKeyRules. These rows are not parity
// rows.
func TestShadowConfigNotMeasured(t *testing.T) {
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
		{"a value of core.sharedrepository that git refuses", []repoSetting{set("core.sharedrepository", "0440")}, ""},
		{"core.sharedrepository in another letter case", []repoSetting{set("core.sharedrepository", "GROUP")}, ""},
		{"a boolean with a sign", []repoSetting{set("core.filemode", "-1")}, ""},
		{"a boolean with a unit letter", []repoSetting{set("core.filemode", "1k")}, ""},
		{"a boolean of ten digits", []repoSetting{set("core.filemode", "0000000001")}, ""},
		{"a word with no value", []repoSetting{{key: "core.eol"}}, ""},
		{"a number with no value", []repoSetting{{key: "checkout.workers"}}, ""},
		{"a text with no value", []repoSetting{{key: "lfs.fetchinclude"}}, ""},
		{"a text with a control byte", []repoSetting{set("lfs.fetchinclude", "a\x01b")}, ""},
		{"a filter with a space", []repoSetting{set("remote.origin.partialclonefilter", "blob:limit 1")}, ""},
		{"a value over 4096 bytes", []repoSetting{set("core.checkstat", strings.Repeat("x", 4097))}, ""},
		{"a URL with another scheme", []repoSetting{set("remote.origin.url", "HTTPS://example.invalid/o/r.git")}, ""},
		{"a URL with a host of one letter", []repoSetting{set("remote.origin.url", "h:o/r.git")}, ""},
		{"a URL with a dash after the scheme", []repoSetting{set("remote.origin.url", "ssh://-x/o/r.git")}, ""},
		{"a URL with a control byte that no cell ran", []repoSetting{set("remote.origin.url", "https://example.invalid/o/a\x02b.git")}, ""},
		{"a branch name with a control character", []repoSetting{set("branch.a\x01b.remote", "origin")}, ""},
		{"an lfs access value of no word", []repoSetting{set("lfs.https://example.invalid/.access", "open")}, ""},
		{"an lfs access of no URL", []repoSetting{set("lfs.x.access", "basic")}, ""},
		{"an extension of an included file", []repoSetting{included("extensions.noop", "true")}, ""},
		{"a filter key of an included file", []repoSetting{included("filter.lfs.required", "true")}, ""},
		{"a key of a section with no cell", []repoSetting{set("lfs.repositoryformatversion", "0")}, ""},
		{"a remote key with no subsection", []repoSetting{set("remote.url", "https://example.invalid/o/r.git")}, ""},
		{"extensions.worktreeconfig", []repoSetting{set("extensions.worktreeconfig", "true")}, ""},
		{"a remote name that starts with a digit", []repoSetting{set("remote.9x.url", "https://example.invalid/o/r.git")},
			"[remote \"9x\"]\n\turl = \"https://example.invalid/o/r.git\"\n"},
		{"a remote name with a part that starts with a dash", []repoSetting{set("remote.a/-b.url", "https://example.invalid/o/r.git")}, ""},
		{"a URL of lfsurl that a rule rewrites", []repoSetting{rule, set("remote.origin.lfsurl", "gh:o/lfs")},
			"[remote \"origin\"]\n\tlfsurl = \"https://example.invalid/o/lfs\"\n" +
				"[url \"https://example.invalid/o/lfs\"]\n\tinsteadOf = \"https://example.invalid/o/lfs\"\n"},
		{
			"a rule of an included file rewrites a remote URL",
			[]repoSetting{included(rule.key, rule.value), set("remote.origin.url", "gh:o/r.git")},
			"[remote \"origin\"]\n\turl = \"https://example.invalid/o/r.git\"\n" +
				"[url \"https://example.invalid/o/r.git\"]\n\tinsteadOf = \"https://example.invalid/o/r.git\"\n",
		},
		{
			"a resolved URL of a form that is left out",
			[]repoSetting{set("url.file:///x/.insteadof", "gh:"), set("remote.origin.url", "gh:o/r.git")},
			"",
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
	// A key of `config.worktree` other than core.bare, and a last core.bare
	// there with a value that is no boolean.
	worktree := []repoSetting{set("core.filemode", "false"), set("core.bare", "yes"), set("core.bare", "maybe")}
	if got, want := shadowConfigText(nil, worktree), shadowConfigTail; got != want {
		t.Errorf("config.worktree:\n got %q\nwant %q", got, want)
	}
}

// worktreesModeOf is the mode call of the folder `worktrees` for the setting
// s alone, through the line of the copy.
func worktreesModeOf(s repoSetting) sharedRepository {
	line, ok := shadowValue(s)
	if !ok {
		return sharedRepository{}
	}
	return sharedRepositoryOf(line)
}

// TestWorktreesChmodValues pins the values of `core.sharedRepository` that
// start the mode call of the folder `worktrees`. Each value whose copy line is
// not `umask` starts it: the 18 rows of group P5 of val-s3p2b2-6-linux, and
// `yes`, `660` and `2` of cells H1 of tier0-15 and S1 and S2 of tier0-16 (Linux
// VM). The rows `1`, `TRUE` and no value are not measured.
func TestWorktreesChmodValues(t *testing.T) {
	for _, row := range []struct {
		value    string
		hasValue bool
		chmod    bool
	}{
		{"group", true, true}, {"true", true, true}, {"all", true, true}, {"world", true, true},
		{"everybody", true, true}, {"0660", true, true}, {"0777", true, true},
		{"yes", true, true}, {"1", true, true}, {"2", true, true}, {"TRUE", true, true},
		{"", false, true}, {"660", true, true}, {"umask", true, false}, {"", true, false}, {"0", true, false},
	} {
		_, ok := worktreesModeOf(repoSetting{key: "core.sharedrepository", value: row.value, hasValue: row.hasValue}).folderMode(os.ModeDir | 0o775)
		if ok != row.chmod {
			t.Errorf("value %q (with a value: %v): mode call %v, want %v", row.value, row.hasValue, ok, row.chmod)
		}
	}
}

// TestSharedRepositoryValues pins the reading of each `core.sharedRepository`
// value: the line of the copy, and the mode of the folder `worktrees` after a
// mkdir with a daemon umask of 022 and of 002. The measured rows are the 18
// rows of group P5 of val-s3p2b2-6-linux (Linux VM): section 0.2 item 6 of its
// report has the table. The rows marked not measured follow sharedRepoLine for
// the line and sharedRepositoryOf for the mode. The last rows are those of
// cell C2 of tier0-14.
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
		{value: "0668", line: "group", u022: 0o2775, u002: 0o2775, notMeasured: true},
		{value: "00660", line: "0660", u022: 0o2770, u002: 0o2770, notMeasured: true},
		{value: "0002", line: "everybody", u022: 0o2775, u002: 0o2775, notMeasured: true},
		{value: "Group", notMeasured: true},
		{value: "maybe", notMeasured: true},
		{value: "off", line: "umask", u022: none, u002: none, notMeasured: true},
		// Cell C2 of tier0-14 (Linux VM) measured the lines of the copy. The
		// mode follows the line. With umask 002 the modes of `yes` (cell H1 of
		// tier0-15), `660` and `2` (cells S1 and S2 of tier0-16) and of the
		// line `0666` (cell C2) are measured. The modes with umask 022, and
		// those of `1`, `TRUE` and no value, are not measured.
		{value: "660", line: "0660", u022: 0o2770, u002: 0o2770},
		{value: "0777", line: "0666", u022: 0o2777, u002: 0o2777},
		{value: "yes", line: "group", u022: 0o2775, u002: 0o2775},
		{value: "1", line: "group", u022: 0o2775, u002: 0o2775},
		{value: "TRUE", line: "group", u022: 0o2775, u002: 0o2775},
		{value: "0", line: "umask", u022: none, u002: none},
		{value: "2", line: "everybody", u022: 0o2775, u002: 0o2775},
		{value: "", line: "umask", u022: none, u002: none},
		{withoutValue: true, line: "group", u022: 0o2775, u002: 0o2775},
	} {
		name := row.value
		if row.notMeasured {
			name += " (not measured)"
		}
		t.Run(name, func(t *testing.T) {
			s := repoSetting{key: "core.sharedrepository", value: row.value, hasValue: !row.withoutValue}
			r := worktreesModeOf(s)
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
