package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The scenarios below reproduce fixtures of the f6010b97 black-box matrix
// (scratch/f6010b97/slice4-matrix.md, appendix). Each fixture follows the
// probe: a tracked `.gitignore` that holds `.claude/worktrees/` plus the
// scenario lines, a tracked `t.txt`, and a tracked manifest. Each untracked
// file holds its own path and a newline. The new worktree is
// `<repo>/.claude/worktrees/w`. The want lists are the f6010b97 file sets.

// includeCase is one fixture of the matrix.
type includeCase struct {
	name string
	// ignore holds the scenario lines of the tracked `.gitignore`.
	ignore string
	// noWorktreesLine leaves `.claude/worktrees/` out of the `.gitignore`.
	noWorktreesLine bool
	// tracked files are committed with the fixture.
	tracked []string
	files   []string
	// manifest is the tracked `.worktreeinclude`. Empty means no manifest.
	manifest string
	// setup runs after the commit and after files are written.
	setup func(t *testing.T, repo string)
	// posixOnly skips the case on Windows (symlinks).
	posixOnly bool
	want      []string
}

// seqf formats the numbers 1 to n with format.
func seqf(n int, format string) []string {
	out := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, fmt.Sprintf(format, i))
	}
	return out
}

func lines(ls ...string) string { return strings.Join(ls, "\n") + "\n" }

func joinAll(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// longpat is "[" + k times "a" + "]" + tail, with total length n. The class
// matches "a".
func longpat(n int, tail string) string {
	return "[" + strings.Repeat("a", n-2-len(tail)) + "]" + tail
}

// classsegs builds lead + "lg/" + class segments + trail with total length n.
// Each class segment matches "a". It returns the pattern and the segment count.
func classsegs(n int, lead, trail string) (string, int) {
	body := n - len(lead) - 3 - len(trail)
	var segs []string
	for body > 0 {
		k := min(40, body)
		if d := body - k; d >= 1 && d <= 3 {
			k = body - 5
		}
		segs = append(segs, "["+strings.Repeat("a", k-2)+"]")
		body -= k + 1
	}
	return lead + "lg/" + strings.Join(segs, "/") + trail, len(segs)
}

func classsegsCase(name string, n int, lead, trail string, opens bool) includeCase {
	p, k := classsegs(n, lead, trail)
	deep := "lg/" + strings.Repeat("a/", k) + "f.txt"
	want := []string{"okd/f.txt"}
	if opens {
		want = append(want, deep)
	}
	return includeCase{name: name, ignore: lines("lg/", "okd/"),
		files: []string{deep, "okd/f.txt"}, manifest: lines(p, "okd/"), want: want}
}

// runIncludeCase builds the fixture, creates the worktree, seeds it, and returns
// the untracked entries of the worktree. An empty directory ends in `/`.
func runIncludeCase(t *testing.T, c includeCase) []string {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "r")
	runGit(t, root, "init", "-q", "-b", "main", "r")
	gi := ".claude/worktrees/\n"
	if c.noWorktreesLine {
		gi = ""
	}
	writeFile(t, filepath.Join(repo, ".gitignore"), gi+c.ignore, 0o644)
	writeFile(t, filepath.Join(repo, "t.txt"), "t.txt\n", 0o644)
	add := []string{"add", ".gitignore", "t.txt"}
	if c.manifest != "" {
		writeFile(t, filepath.Join(repo, worktreeIncludeFile), c.manifest, 0o644)
		add = append(add, worktreeIncludeFile)
	}
	for _, f := range c.tracked {
		writeFile(t, filepath.Join(repo, filepath.FromSlash(f)), f+"\n", 0o644)
		add = append(add, f)
	}
	runGit(t, repo, add...)
	runGit(t, repo, "commit", "-q", "-m", "init")
	for _, f := range c.files {
		writeFile(t, filepath.Join(repo, filepath.FromSlash(f)), f+"\n", 0o644)
	}
	if c.setup != nil {
		c.setup(t, repo)
	}
	wt := filepath.Join(repo, ".claude", "worktrees", "w")
	runGit(t, repo, "worktree", "add", "-q", "-b", "w", wt)
	populateWorktree(repo, wt)
	return worktreeEntries(t, wt)
}

// worktreeEntries lists the untracked files and the empty directories of wt.
func worktreeEntries(t *testing.T, wt string) []string {
	t.Helper()
	tracked := map[string]bool{".git": true, ".gitignore": true, "t.txt": true, worktreeIncludeFile: true}
	var out []string
	err := filepath.WalkDir(wt, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(wt, p)
		rel = filepath.ToSlash(rel)
		if rel == "." || tracked[rel] {
			return nil
		}
		if d.IsDir() {
			if ents, _ := os.ReadDir(p); len(ents) == 0 {
				out = append(out, rel+"/")
			}
			return nil
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

func checkIncludeCases(t *testing.T, cases []includeCase) {
	t.Helper()
	requireGit(t)
	isolateGitConfig(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if c.posixOnly && runtime.GOOS == "windows" {
				t.Skip("symlink creation needs privileges on Windows")
			}
			got := slices.DeleteFunc(runIncludeCase(t, c), func(e string) bool { return slices.Contains(c.tracked, e) })
			want := slices.Clone(c.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("copied %d entries:\n  %v\nwant %d entries (f6010b97):\n  %v", len(got), got, len(want), want)
			}
		})
	}
}

// Fixtures shared by several scenarios, as named in the matrix.
var (
	buildTreeIgnore = lines("build/")
	buildTreeFiles  = []string{"build/a.txt", "build/x/b.txt", "build/x/y/c.txt", "build/.h/d.txt"}
	bld2Ignore      = lines("build/")
	bld2Files       = []string{"build/a.txt", "sub/build/b.txt", "a/x/build/c.txt"}
	bld2All         = bld2Files
	bld3Ignore      = lines("build/", ".bx/")
	bld3Files       = append(slices.Clone(bld2Files), ".bx/d.txt")
	nmIgnore        = lines("node_modules/")
	nmFiles         = []string{"node_modules/x.json", "pkg/node_modules/x.json", "a/b/node_modules/x.json"}
	dotdIgnore      = lines(".envd/")
	dotdFiles       = []string{".envd/a.txt", "sub/.envd/b.txt"}
)

// TestIncludeOpeningRules covers R4: only the ignored directories that the
// manifest opens are searched. It also covers the controls where every build
// agrees.
func TestIncludeOpeningRules(t *testing.T) {
	bt := func(name, manifest string, want ...string) includeCase {
		return includeCase{name: name, ignore: buildTreeIgnore, files: buildTreeFiles, manifest: manifest, want: want}
	}
	b2 := func(name, manifest string, want ...string) includeCase {
		return includeCase{name: name, ignore: bld2Ignore, files: bld2Files, manifest: manifest, want: want}
	}
	nm := func(name, manifest string, want ...string) includeCase {
		return includeCase{name: name, ignore: nmIgnore, files: nmFiles, manifest: manifest, want: want}
	}
	checkIncludeCases(t, []includeCase{
		bt("B01_dir_explicit", "build/\n", buildTreeFiles...),
		bt("B02_file_in_ignored_dir", "build/a.txt\n", "build/a.txt"),
		bt("B03_anydepth_ext", "*.txt\n"),
		bt("B04_anydepth_basename", "b.txt\n"),
		bt("B05_dir_doublestar_file", "build/**/c.txt\n", "build/x/y/c.txt"),
		bt("B06_leading_doublestar", "**/b.txt\n"),
		bt("B07_deep_path", "build/x/y/c.txt\n", "build/x/y/c.txt"),
		bt("B08_glob_dir_prefix", "bu*/a.txt\nb?ild/x/b.txt\n[b]uild/x/y/c.txt\n",
			"build/a.txt", "build/x/b.txt", "build/x/y/c.txt"),
		bt("B15_star_matches_dir", "*\n"),
		bt("B16_doublestar_only", "**\n"),
		bt("B17_dir_trailing_doublestar", "build/**\n", buildTreeFiles...),
		bt("B19_wildcard_mid_dir", "build/*/b.txt\n", "build/x/b.txt"),
		{name: "B11_nested_ignored_in_untracked", ignore: nmIgnore,
			files:    []string{"pkg/node_modules/x/cfg.json", "pkg/src.txt", "node_modules/y/cfg.json"},
			manifest: "pkg/node_modules/x/cfg.json\nnode_modules/\n",
			want:     []string{"node_modules/y/cfg.json", "pkg/node_modules/x/cfg.json"}},
		{name: "B12_anydepth_in_nested_ignored", ignore: nmIgnore,
			files:    []string{"pkg/node_modules/x/cfg.json", "node_modules/y/cfg.json", "a/b/c/node_modules/z/cfg.json"},
			manifest: "cfg.json\n"},
		{name: "B18_ignored_by_pattern_file_glob", ignore: lines("*.cache/"),
			files: []string{"a.cache/f.txt", "b.cache/g.txt"}, manifest: "a.cache/\n*.txt\n",
			want: []string{"a.cache/f.txt"}},
		nm("X01_nm_trailing_slash", "node_modules/\n", nmFiles...),
		b2("X02_build_no_slash", "build\n", bld2All...),
		b2("X03_build_anchored", "/build/\n", "build/a.txt"),
		nm("X04_star_seg_nm_file", "*/node_modules/x.json\n", "pkg/node_modules/x.json"),
		nm("X05_dstar_nm_file", "**/node_modules/x.json\n", nmFiles...),
		b2("X06_neg_only", "!build/\n"),
		b2("X07_pos_then_neg", "build/\n!build/\n"),
		bt("X08_subdir_of_ignored", "build/x/\n", "build/x/b.txt", "build/x/y/c.txt"),
		{name: "X10_glob_first_seg_file", ignore: lines("*.cache/"),
			files: []string{"a.cache/f.txt", "b.cache/g.txt", "sub/c.cache/h.txt"}, manifest: "*.cache/f.txt\n",
			want: []string{"a.cache/f.txt"}},
		b2("X11_glob_no_slash", "b*\n", "build/a.txt"),
		b2("X12_class_dir", "[ab]uild/\n"),
		b2("X13_dstar_dir", "**/build/\n", bld2All...),
		b2("X14_mid_dstar_dir", "a/**/build/\n", "a/x/build/c.txt"),
		b2("X15_nested_explicit", "sub/build/\n", "sub/build/b.txt"),
		b2("X16_nested_explicit_file", "a/x/build/c.txt\n", "a/x/build/c.txt"),
		{name: "X17b_escaped_bracket_dir", ignore: lines(`we\[i\]rd/`, "weird/"),
			files: []string{"we[i]rd/f.txt", "weird/g.txt"}, manifest: `we\[i\]rd/` + "\n",
			want: []string{"we[i]rd/f.txt"}},
		{name: "X18_literal_bracket_dir", ignore: lines("we[i]rd/", "weird/"),
			files: []string{"we[i]rd/f.txt", "weird/g.txt"}, manifest: "we[i]rd/\n",
			want: []string{"weird/g.txt"}},
		{name: "X26_dotdir_symlink", ignore: lines(".ld", "real/"), files: []string{"real/a.txt"},
			manifest: "*.txt\n", posixOnly: true,
			setup: func(t *testing.T, repo string) {
				if err := os.Symlink("real", filepath.Join(repo, ".ld")); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "X27_dotdir_in_ignored_nondot", ignore: lines("build/"),
			files: []string{"build/.envd/a.txt", "build/b.txt"}, manifest: "*.txt\n"},
		{name: "X29_dir_ignored_by_star", ignore: lines("*.d"),
			files: []string{"conf.d/a.txt", "conf.d/s/b.txt"}, manifest: "conf.d/a.txt\n",
			want: []string{"conf.d/a.txt"}},
		{name: "X30_dir_ignored_by_star_anydepth", ignore: lines("*.d"),
			files: []string{"conf.d/a.txt", ".conf.d/a.txt"}, manifest: "a.txt\n",
			want: []string{".conf.d/a.txt"}},
		{name: "X33_partial_dir_ignore", ignore: lines("keep/*"),
			files: []string{"keep/a.txt", "keep/s/b.txt"}, manifest: "*.txt\n",
			want: []string{"keep/a.txt"}},
		bt("X34_trailing_space_dir", "build/   \n", buildTreeFiles...),
		bt("X35_crlf_dir", "build/\r\n", buildTreeFiles...),
		bt("X36_bom_dir", "\ufeffbuild/\n", buildTreeFiles...),
		bt("X37_comment_dir", "#build/\n"),
		bt("X38_dotslash_dir", "./build/\n"),
		bt("X39_double_slash", "build//a.txt\n"),
		{name: "X42_escaped_hash_dir", ignore: lines(`\#h/`), files: []string{"#h/a.txt"}, manifest: `\#h/` + "\n"},
		{name: "X43_negated_dir_pattern_literal", ignore: lines(`\!b/`), files: []string{"!b/a.txt"}, manifest: `\!b/` + "\n"},
		{name: "X47_listing_ignored_file_in_nondot_dir", ignore: lines("*.log", "build/"),
			files: []string{"x/y/z.log", "build/b.log"}, manifest: "*.log\n", want: []string{"x/y/z.log"}},
		{name: "Y40_prefix_vs_glob", ignore: lines("/build/", "/bxyz/", "/sub/bq/", "/bd/"),
			files:    []string{"build/a.txt", "bxyz/b.txt", "sub/bq/c.txt", "bd/d.txt"},
			manifest: "b*d/\n", want: []string{"bd/d.txt", "build/a.txt"}},
		{name: "Y41_prefix_slash_crossing", ignore: lines("/sub/build/", "/subx/"),
			files: []string{"sub/build/a.txt", "subx/b.txt"}, manifest: "su*\n",
			want: []string{"sub/build/a.txt", "subx/b.txt"}},
	})
}

// TestIncludeOpeningShapes covers the Y family: one fixture, one pattern per
// case. Git ignores `build/` at three depths and the dot directory `.bx/`.
func TestIncludeOpeningShapes(t *testing.T) {
	y := func(pattern string, want ...string) includeCase {
		return includeCase{name: pattern, ignore: bld3Ignore, files: bld3Files, manifest: pattern + "\n", want: want}
	}
	root := "build/a.txt"
	checkIncludeCases(t, []includeCase{
		y("[b]uild/"),
		y("bu[i]ld/", root),
		y("b[a-z]ild/", root),
		y("?uild/"),
		y("*uild/"),
		y("bui*/", root),
		y("build*/", root),
		y("[ab]uild"),
		y("b*/", root),
		y("*/build/", "sub/build/b.txt"),
		y("sub/b*/", "sub/build/b.txt"),
		y("sub/[b]uild/", "sub/build/b.txt"),
		y("**/b*/"),
		y("**/[ab]uild/"),
		y(`bu\ild/`, root),
		y("buil?", root),
		y("[!a]uild/"),
		y("[[:alpha:]]uild/"),
		y("b*", root),
		y("bu*ld", root),
		y("build/", bld2All...),
		y("build", bld2All...),
		y("/b*/", root),
		y("a/*/build/", "a/x/build/c.txt"),
		y("a/x/b*/", "a/x/build/c.txt"),
		y("*", ".bx/d.txt"),
		y("?"),
		y("**", ".bx/d.txt"),
		y("b**", root),
		y("**b"),
		y(".b*", ".bx/d.txt"),
		y(".bx", ".bx/d.txt"),
		y("*.txt", ".bx/d.txt"),
		y("d.txt", ".bx/d.txt"),
		y(`\build/`),
		y(`b\*/`),
	})
}

// TestIncludeAnyDepthDotDirs covers R5 to R7: the any-depth flag, the 128 cap
// with the place of `.claude/`, and the skip list.
func TestIncludeAnyDepthDotDirs(t *testing.T) {
	d := func(name, manifest string, want ...string) includeCase {
		return includeCase{name: name, ignore: dotdIgnore, files: dotdFiles, manifest: manifest, want: want}
	}
	// dotDirs follows the matrix: each name is ignored as `/<name>/` and holds f.dd.
	dotDirs := func(name string, names []string, manifest string, want []string) includeCase {
		var ig, files []string
		for _, n := range names {
			ig = append(ig, "/"+n+"/")
			files = append(files, n+"/f.dd")
		}
		return includeCase{name: name, ignore: lines(ig...), files: files, manifest: manifest, want: want}
	}
	fdd := func(names []string) []string {
		out := make([]string, len(names))
		for i, n := range names {
			out[i] = n + "/f.dd"
		}
		return out
	}
	d127, d128, d129 := seqf(127, ".d%03d"), seqf(128, ".d%03d"), seqf(129, ".d%03d")

	f02Names := joinAll(seqf(64, ".d%03d"), seqf(65, "sub/.e%03d"))
	f02 := includeCase{name: "F02_dotcap_129_nested", manifest: "*.dd\n", files: fdd(f02Names),
		want: fdd(f02Names[:127])}
	for _, n := range f02Names {
		f02.ignore += n + "/\n"
	}
	f05 := dotDirs("F05_dotcap_129_plus_nondot", d129, "*.dd\nbuild/\n", append(fdd(d127), "build/f.dd"))
	f05.ignore += "build/\n"
	f05.files = append(f05.files, "build/f.dd")
	f06 := dotDirs("F06_dotcap_128_claude_not_ignored", d128, "*.dd\n", fdd(d128))
	f06.noWorktreesLine = true
	f08 := dotDirs("F08_dotcap_ignored_claude_files", d127, "*.dd\n", append(fdd(d127), ".claude/x.dd"))
	f08.ignore = ".claude/\n" + f08.ignore
	f08.files = append(f08.files, ".claude/x.dd")

	skip := []string{".angular", ".cache", ".dart_tool", ".gradle", ".next", ".nuxt", ".parcel-cache",
		".pnpm-store", ".svelte-kit", ".terraform", ".tox", ".turbo", ".venv", ".yarn"}
	// Every skip-list name in an odd case, next to one near name that is opened.
	var oddCase []string
	for _, n := range skip {
		oddCase = append(oddCase, "."+strings.ToUpper(n[1:2])+n[2:])
	}
	near := []string{".venv2", ".venv-x", ".cache2", ".yarn-x", ".next.js", "._venv", ".tox_", ".gradle-x",
		".terraform.d", ".nuxt2", ".turbo2", ".angular2", ".dart_tool2", ".svelte-kit2", ".pnpm-store2",
		".parcel-cache2"}

	checkIncludeCases(t, []includeCase{
		{name: "B14_dotdir_anydepth", ignore: dotdIgnore,
			files: []string{".envd/a.txt", ".envd/s/b.txt", "sub/.envd/c.txt"}, manifest: "*.txt\n",
			want: []string{".envd/a.txt", ".envd/s/b.txt", "sub/.envd/c.txt"}},
		d("X19_star_dotdir", "*\n", dotdFiles...),
		d("X20_dstar_dotdir", "**\n", dotdFiles...),
		d("X21_basename_dotdir", "a.txt\n", ".envd/a.txt"),
		d("X22_lead_dstar_dotdir", "**/b.txt\n", "sub/.envd/b.txt"),
		d("X23_slash_pattern_dotdir", "x/**/a.txt\n"),
		d("X24_neg_anydepth_dotdir", "!*.txt\n"),
		d("X25_dotstar_pattern", ".*\n", dotdFiles...),
		d("X31_dotdir_dstar_suffix", ".envd/**\n", ".envd/a.txt"),
		d("X32_dotdir_anydepth_pattern_with_dotdir", "**/.envd/\n", dotdFiles...),
		d("X40_bom_dotdir_anydepth", "\ufeff*.txt\n", dotdFiles...),
		d("X41_crlf_dotdir_anydepth", "*.txt\r\n", dotdFiles...),
		d("X45_dotdir_explicit_file", ".envd/a.txt\n", ".envd/a.txt"),
		{name: "X46_dotdir_opened_but_file_not_ignored_standard", ignore: dotdIgnore,
			files: append(slices.Clone(dotdFiles), "plain/u.txt"), manifest: "*.txt\n", want: dotdFiles},
		{name: "X48_nested_ignored_inside_dotdir", ignore: dotdIgnore,
			files:    []string{".envd/node_modules/m.txt", ".envd/.venv/v.txt", ".envd/a.txt"},
			manifest: "*.txt\n", want: []string{".envd/node_modules/m.txt", ".envd/.venv/v.txt", ".envd/a.txt"}},
		{name: "X49_dotdir_under_skipped", ignore: lines(".venv/"), files: []string{".venv/.inner/a.txt"},
			manifest: "*.txt\n"},
		{name: "C03_claude_whole_ignored_anydepth", ignore: lines(".claude/"),
			files: []string{".claude/settings.local.json", ".claude/a/b.json", ".claude/checkpoints/c.json",
				"sub/.claude/d.json", "sub/.claude/checkpoints/e.json"},
			manifest: "*.json\n",
			want:     []string{".claude/a/b.json", ".claude/settings.local.json", "sub/.claude/d.json"}},
		{name: "Z01_stale_worktree_dir_newgit", ignore: lines("top.txt"),
			files:    []string{".claude/worktrees/stale/x.txt", ".claude/worktrees/stale/sub/y.txt", "top.txt"},
			manifest: "*.txt\n", want: []string{"top.txt"}},
		{name: "Z03_stale_worktree_explicit", files: []string{".claude/worktrees/stale/x.txt"},
			manifest: ".claude/worktrees/\n.claude/\n"},

		dotDirs("F01_dotcap_127", d127, "*.dd\n", fdd(d127)),
		dotDirs("F01_dotcap_128", d128, "*.dd\n", fdd(d127)),
		dotDirs("F01_dotcap_129", d129, "*.dd\n", fdd(d127)),
		f02,
		dotDirs("F03_dotcap_128_plus_skipped", joinAll([]string{".venv", ".cache"}, d128), "*.dd\n", fdd(d127)),
		dotDirs("F04_dotcap_129_explicit_extra", d129, "*.dd\n.d129/\n", append(fdd(d127), ".d129/f.dd")),
		f05,
		f06,
		dotDirs("F07_dotcap_128_names_before_claude", seqf(128, ".a%03d"), "*.dd\n", fdd(seqf(128, ".a%03d"))),
		f08,
		dotDirs("F11_skip_case_variants", []string{".VENV", ".Cache", ".Parcel-Cache", ".NEXT"}, "*.dd\n", nil),
		dotDirs("skip_list_all_14_odd_case", joinAll(oddCase, []string{".okdir"}), "*.dd\n", []string{".okdir/f.dd"}),
		{name: "F12_skip_nested", manifest: "*.dd\n",
			ignore: lines("sub/.venv/", "sub/.cache/", "sub/.parcel-cache/", "sub/.okdir/"),
			files:  []string{"sub/.venv/f.dd", "sub/.cache/f.dd", "sub/.parcel-cache/f.dd", "sub/.okdir/f.dd"},
			want:   []string{"sub/.okdir/f.dd"}},
		dotDirs("F13_skip_explicit", []string{".venv", ".cache", ".parcel-cache"},
			".venv/\n.cache/f.dd\n.parcel-cache/**\n", []string{".venv/f.dd", ".cache/f.dd", ".parcel-cache/f.dd"}),
		dotDirs("F14_skip_exact_names_variants", near, "*.dd\n", fdd(near)),
		dotDirs("F15_nondot_named_like_skip", []string{"venv", "cache", "node_modules"}, "*.dd\n", nil),
	})
}

// TestIncludePatternCaps covers R8 to R10: the 256 counted patterns, the
// 1024-byte length cap and the 32-segment cap, with each boundary pair.
func TestIncludePatternCaps(t *testing.T) {
	// manyDirs follows the matrix: dNNN/ ignored by `/d*/`, each with f.txt.
	manyDirs := func(name string, n int, manifest func(lines []string) string, want []string) includeCase {
		dirs := seqf(n, "d%03d/")
		return includeCase{name: name, ignore: lines("/d*/"), files: seqf(n, "d%03d/f.txt"),
			manifest: manifest(dirs), want: want}
	}
	plain := func(l []string) string { return strings.Join(l, "\n") + "\n" }
	cp := func(n int) []string { return seqf(n, "p%03d.cp") }

	e04 := func(l []string) string {
		return plain(slices.Insert(slices.Clone(l), 100, "!zzz/"))
	}
	e05 := manyDirs("E05_count_257_plus_dotdir", 257,
		func(l []string) string { return plain(l) + "*.txt\nok.ig\n" },
		joinAll(seqf(256, "d%03d/f.txt"), []string{".envd/a.txt", "ok.ig"}))
	e05.ignore += ".envd/\n*.ig\n"
	e05.files = append(e05.files, ".envd/a.txt", "ok.ig")

	lg := func(name, pattern string, opens bool) includeCase {
		want := []string{"okd/f.txt"}
		if opens {
			want = append(want, "lg/ax/f.txt")
		}
		return includeCase{name: name, ignore: lines("lg/", "okd/"), files: []string{"lg/ax/f.txt", "okd/f.txt"},
			manifest: pattern + "\nokd/\n", want: want}
	}
	e10 := func(n int) includeCase {
		p1, p2 := "lg/"+longpat(n-3, "x/"), longpat(n, "x.lf")
		return includeCase{name: fmt.Sprintf("E10_len_%d", n), ignore: lines("lg/", "*.lf", "okd/"),
			files:    []string{"lg/ax/f.txt", "ax.lf", "ok.lf", "okd/f.txt"},
			manifest: lines(p1, p2, "ok.lf", "okd/"),
			want:     []string{"ax.lf", "lg/ax/f.txt", "ok.lf", "okd/f.txt"}}
	}
	seg := func(n int, opens bool) includeCase {
		segs := append([]string{"deep"}, seqf(n-2, "s%d")...)
		segs = append(segs, "f.txt")
		deep := strings.Join(segs, "/")
		fsegs := append(seqf(n-1, "s%d"), "f.sg")
		sg := strings.Join(fsegs, "/")
		want := []string{"ok/f.txt", sg}
		if opens {
			want = append(want, deep)
		}
		return includeCase{name: fmt.Sprintf("E20_seg_%d", n), ignore: lines("deep/", "*.sg", "ok/"),
			files: []string{deep, sg, "ok/f.txt"}, manifest: lines(deep, sg, "ok/"), want: want}
	}
	deepDir := func(name string, n int, pattern string, opens bool) includeCase {
		f := "deep/" + strings.Join(seqf(n, "s%d"), "/") + "/f.txt"
		c := includeCase{name: name, ignore: lines("deep/"), files: []string{f}, manifest: pattern + "\n"}
		if opens {
			c.want = []string{f}
		}
		return c
	}
	dstar := func(name string, k int, opens bool) includeCase {
		c := includeCase{name: name, ignore: lines("deep/"), files: []string{"deep/s/f.txt"},
			manifest: "deep/" + strings.Repeat("**/", k) + "f.txt\n"}
		if opens {
			c.want = []string{"deep/s/f.txt"}
		}
		return c
	}
	s31 := "deep/" + strings.Join(seqf(31, "s%d"), "/")
	s30 := "deep/" + strings.Join(seqf(30, "s%d"), "/")

	checkIncludeCases(t, []includeCase{
		manyDirs("E01_count_dirs_256", 256, plain, seqf(256, "d%03d/f.txt")),
		manyDirs("E01_count_dirs_257", 257, plain, seqf(256, "d%03d/f.txt")),
		{name: "E02_count_files_257", ignore: lines("*.cp"), files: cp(257), manifest: plain(cp(257)), want: cp(257)},
		manyDirs("E03_count_comments_blank_256", 256,
			func(l []string) string {
				return strings.Repeat("# c\n", 10) + strings.Repeat("\n", 10) + strings.Repeat("   \n", 5) + plain(l)
			}, seqf(256, "d%03d/f.txt")),
		manyDirs("E04_count_negation_at_100", 256, e04, seqf(255, "d%03d/f.txt")),
		e05,
		{name: "E06_count_300_filepatterns_then_dir", ignore: lines("*.cp", "build/"),
			files: append(cp(300), "build/a.txt"), manifest: plain(cp(300)) + "build/\n", want: cp(300)},
		{name: "E07_count_dir_first_then_300", ignore: lines("*.cp", "build/"),
			files: append(cp(300), "build/a.txt"), manifest: "build/\n" + plain(cp(300)),
			want: append(cp(300), "build/a.txt")},
		{name: "E08_300_files_then_anydepth", ignore: lines("*.cp", ".envd/"),
			files: append(cp(300), dotdFiles...), manifest: plain(cp(300)) + "*.txt\n",
			want: append(cp(300), dotdFiles...)},
		manyDirs("E09_segcap_pattern_counts", 256,
			func(l []string) string { return strings.Join(seqf(33, "q%d"), "/") + "\n" + plain(l) },
			seqf(256, "d%03d/f.txt")),
		manyDirs("E42_len_over_counts", 256,
			func(l []string) string { return "zz/" + strings.Repeat("q", 1097) + "\n" + plain(l) },
			seqf(256, "d%03d/f.txt")),

		e10(1025),
		lg("E11_len_1024_plus_trailing_spaces", "lg/"+longpat(1021, "x/")+"   ", true),
		lg("E12_len_1024_crlf", "lg/"+longpat(1021, "x/")+"\r", true),
		lg("E30_len_1100", "lg/"+longpat(1097, "x/"), false),
		lg("E30_len_65536", "lg/"+longpat(65533, "x/"), false),
		lg("E31_len_2048_star_pad", "lg/"+strings.Repeat("*", 2045), false),
		lg("E40_len1seg_1026", "lg/"+longpat(1023, "x/"), false),
		classsegsCase("E43_len_classsegs_1025", 1025, "", "/", true),
		classsegsCase("E43_len_classsegs_1026", 1026, "", "/", false),
		classsegsCase("E44_len_classsegs_notrail_1024", 1024, "", "", true),
		classsegsCase("E44_len_classsegs_notrail_1025", 1025, "", "", false),
		classsegsCase("E45_len_classsegs_lead_1026", 1026, "/", "/", true),
		{name: "E32_len_long_anydepth_dot", ignore: dotdIgnore, files: dotdFiles,
			manifest: longpat(4096, "a.txt") + "\n"},

		seg(32, true),
		seg(33, false),
		deepDir("E21_seg_33_dir_pattern", 31, s31+"/", true),
		deepDir("E22_seg_32_dir_pattern", 30, s30+"/", true),
		deepDir("E23_seg_33_leading_slash", 30, "/"+s30+"/f.txt", true),
		dstar("E24_seg_33_with_dstar", 31, false),
		dstar("E25_seg_32_with_dstar", 30, true),
		{name: "E26_seg_33_double_slashes", ignore: lines("deep/"), files: []string{"deep/s/f.txt"},
			manifest: "deep/s" + strings.Repeat("/", 32) + "f.txt\n"},
		{name: "E27_seg_33_file_pattern_anydepth_dot", ignore: dotdIgnore, files: dotdFiles,
			manifest: strings.Repeat("**/", 32) + "a.txt\n"},
	})
}

// TestIncludeManifestAndCopyDetails covers R1 and R16, including the two older
// gaps D10 (a symlinked manifest) and D16 (a nested repository).
func TestIncludeManifestAndCopyDetails(t *testing.T) {
	checkIncludeCases(t, []includeCase{
		{name: "A00_no_manifest", ignore: lines("*.i"), files: []string{"a.i", "sub/a.i", "u.txt"}},
		{name: "A01_basic", ignore: lines("*.i"), files: []string{"a.i", "sub/a.i", "b.i", "u.txt"},
			manifest: "a.i\nu.txt\n", want: []string{"a.i", "sub/a.i"}},
		{name: "D10_manifest_symlink", ignore: lines("*.i"), files: []string{"a.i"}, posixOnly: true,
			setup: func(t *testing.T, repo string) {
				writeFile(t, filepath.Join(repo, "real.inc"), "a.i\n", 0o644)
				if err := os.Symlink("real.inc", filepath.Join(repo, worktreeIncludeFile)); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "D11_manifest_is_dir", ignore: lines("*.i"), files: []string{"a.i", ".worktreeinclude/x"}},
		{name: "D12_manifest_empty", ignore: lines("*.i"), files: []string{"a.i"},
			setup: func(t *testing.T, repo string) {
				writeFile(t, filepath.Join(repo, worktreeIncludeFile), "", 0o644)
			}},
		{name: "D16_nested_repo_in_ignored", ignore: lines("vendor/"),
			files: []string{"vendor/lib/a.txt", "vendor/plain.txt"}, manifest: "vendor/\n*.txt\n",
			setup: func(t *testing.T, repo string) {
				runGit(t, filepath.Join(repo, "vendor", "lib"), "init", "-q")
			},
			want: []string{"vendor/plain.txt"}},
	})
}

// TestVersionSelectsIncludeScan covers the R2 parse table, measured with a
// fake `git version` answer.
func TestVersionSelectsIncludeScan(t *testing.T) {
	for out, newScan := range map[string]bool{
		"git version 2.31.9":                 false,
		"git version 2.32.0":                 true,
		"git version 2.31.99.windows.1":      false,
		"git version 2.32.0.windows.1":       true,
		"git version 3.0.0":                  true,
		"git version 1.99.0":                 false,
		"git version 2.100.0":                true,
		"git version 2.4.0":                  false,
		"garbage":                            false,
		"":                                   false,
		"git version 2.32":                   true,
		"git version 2.31":                   false,
		"git version 2.32.0-rc0":             true,
		"git version 2.31.1 (Apple Git-130)": false,
		"git version 2.50.1 (Apple Git-155)": true,
		// The interpretation matrix: each `git version` answer, byte for byte,
		// with the scan that f6010b97 picked on Linux and macOS.
		"git version 2.32.0\n":                     true,
		"git version 2.31.0\n":                     false,
		"git version 2\n":                          false,
		"git version 3\n":                          false,
		"git version 10\n":                         false,
		"git version 2.\n":                         false,
		"git version 3.\n":                         false,
		"2.32.0\n":                                 false,
		"3.0.0\n":                                  false,
		"Git version 2.32.0\n":                     false,
		"GIT VERSION 2.32.0\n":                     false,
		"version 2.32.0\n":                         false,
		"git version v2.32.0\n":                    false,
		"git  version 2.32.0\n":                    false,
		"git version  2.32.0\n":                    false,
		"git version\t2.32.0\n":                    false,
		"git version 2.32.0\r\n":                   true,
		"git version 2.32.0   \n":                  true,
		"git version 02.032.0\n":                   true,
		"git version 2.32abc\n":                    true,
		"git version 2.31.0\ngit version 2.32.0\n": false,
		"git version 2.32.0\ngit version 2.31.0\n": true,
		" git version 2.32.0\n":                    true,
		"  git version 2.32.0\n":                   true,
		"\tgit version 2.32.0\n":                   true,
		"\ngit version 2.32.0\n":                   true,
		"xx git version 2.32.0\n":                  true,
		"git version 99999999999999999999.0\n":     true,
		"git version 2.99999999999999999999\n":     true,
	} {
		if got := versionSelectsIncludeScan(out); got != newScan {
			t.Errorf("versionSelectsIncludeScan(%q) = %v, want %v", out, got, newScan)
		}
	}
}

// TestIncludeOpenerIgnoresCase covers the case rule of R4 without a
// case-insensitive file system. `BUILD/` opens `build` and `build/` opens
// `Build`, as measured on APFS (X09, X44). X09b shows the same opening with
// core.ignorecase=false.
func TestIncludeOpenerIgnoresCase(t *testing.T) {
	for _, c := range []struct {
		manifest, dir string
		opens         bool
	}{
		{"BUILD/\n", "build", true},
		{"BUILD/\n", "sub/build", true},
		{"build/\n", "Build", true},
		{"**/BUILD/\n", "a/x/build", true},
		{"B*/\n", "build", true},
		{"SUB/B*/\n", "sub/build", true},
		{"build/\n", "builds", false},
		{"b*/\n", "sub/build", false},
	} {
		plan := parseIncludePlan([]byte(c.manifest))
		if got := slices.Contains(openIncludeDirs(plan, []string{c.dir}), c.dir); got != c.opens {
			t.Errorf("manifest %q opens %q = %v, want %v", c.manifest, c.dir, got, c.opens)
		}
	}
}

// TestIncludePlanOpensNothing covers shapes that the logs show open no
// directory. Git does not match most of them either, so a file set alone does
// not show the rule. The dot directory `.bx` shows the any-depth flag.
func TestIncludePlanOpensNothing(t *testing.T) {
	dirs := []string{".bx", "a", "a/x", "a/x/build", "build", "sub", "sub/build"}
	for manifest, want := range map[string][]string{
		"./build/\n":     nil,              // X38
		"!build/\n":      nil,              // X06: a negation opens nothing
		"!*.txt\n":       nil,              // X24: a negation sets no flag
		"x/**/a.txt\n":   nil,              // X23: an inner `/` sets no flag
		"**/b*/\n":       {".bx"},          // Y12: `**/` and a glob opens no non-dot dir
		"**/[ab]uild/\n": {".bx"},          // Y13
		"*.txt\n":        {".bx"},          // B03, Y32
		"b*/\n":          {".bx", "build"}, // Y08: root level only
	} {
		got := openIncludeDirs(parseIncludePlan([]byte(manifest)), dirs)
		if !slices.Equal(got, want) {
			t.Errorf("manifest %q opens %v, want %v", manifest, got, want)
		}
	}
}

// TestIncludeGlobSegments covers the glob forms that the opening rules match
// against one directory name.
func TestIncludeGlobSegments(t *testing.T) {
	for _, c := range []struct {
		glob, name string
		match      bool
	}{
		{"b[a-z]ild", "build", true},
		{"b[!u]ild", "build", false},
		{"b[^x]ild", "build", true},
		{"b[[:alpha:]]ild", "build", true},
		{"b[[:digit:]]ild", "build", false},
		{`bu\ild`, "build", true},
		{`we\[i\]rd`, "we[i]rd", true},
		{"we[i]rd", "weird", true},
		{"a[]]b", "a]b", true},
		{`a[\]]b`, "a]b", true},
		{"a[^]]b", "a]b", false},
		{"b*d", "bxyz", false},
		{"b??ld", "build", true},
		{"b[a-z", "build", false},
		{"b[[:alpha:", "build", false},
		{`b\`, "b", false},
	} {
		re := compileSegmentGlob(c.glob)
		if got := re != nil && re.MatchString(c.name); got != c.match {
			t.Errorf("glob %q on %q = %v, want %v", c.glob, c.name, got, c.match)
		}
	}
}

// TestTrimTrailingSpaces covers the space rule. Unescaped trailing spaces go
// (X34, E11), a tab stays (A20) and a backslash keeps one space (A08).
func TestTrimTrailingSpaces(t *testing.T) {
	for in, want := range map[string]string{
		"build/   ":  "build/",
		"build/\t":   "build/\t",
		`trail.i\ `:  `trail.i\ `,
		`trail.i\  `: `trail.i\ `,
		`x\\ `:       `x\\`,
		"   ":        "",
		"plain":      "plain",
	} {
		if got := trimTrailingSpaces(in); got != want {
			t.Errorf("trimTrailingSpaces(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestIncludeFilesOverLimit covers the R11 boundary. At 1 048 576 bytes of
// (length + 3) the new scan runs. At 1 048 577 the old scan runs.
func TestIncludeFilesOverLimit(t *testing.T) {
	files := make([]string, 10180)
	for i := range files {
		files[i] = strings.Repeat("p", 100)
	}
	at := append(slices.Clone(files), strings.Repeat("q", 33))
	over := append(slices.Clone(files), strings.Repeat("q", 34))
	if includeFilesOverLimit(at) {
		t.Error("1 048 576 bytes falls back, want the new scan")
	}
	if !includeFilesOverLimit(over) {
		t.Error("1 048 577 bytes keeps the new scan, want the fallback")
	}
}

// TestIncludeFallbackAboveOneMiB covers R11 end to end, with the M fixture:
// `build/` ignored, the manifest `*.txt`, and ignored `*.pad` files as padding.
// The manifest also names the last pad file, so the copy reaches the last
// batch of the new scan (R17).
func TestIncludeFallbackAboveOneMiB(t *testing.T) {
	padNames := func(last int) []string {
		names := make([]string, 0, 10181)
		for i := range 10180 {
			stem := fmt.Sprintf("P%06d", i)
			names = append(names, stem+strings.Repeat("x", 100-len(stem)-4)+".pad")
		}
		stem := "Q000000"
		return append(names, stem+strings.Repeat("x", last-len(stem)-4)+".pad")
	}
	arm := func(name string, last int, fallback bool) includeCase {
		pads := padNames(last)
		want := []string{pads[len(pads)-1]}
		if fallback {
			want = append(want, buildTreeFiles...)
		}
		return includeCase{name: name, ignore: buildTreeIgnore + "*.pad\n",
			files: append(slices.Clone(buildTreeFiles), pads...), manifest: "*.txt\n" + pads[len(pads)-1] + "\n",
			want: want}
	}
	checkIncludeCases(t, []includeCase{
		arm("at_1048576_new_scan", 33, false),
		arm("at_1048577_fallback", 34, true),
	})
}

// TestGitVersionCmdArgv pins the `git version` call as measured against
// f6010b97: no `-c` options, no `-C`, and the daemon's own working directory.
func TestGitVersionCmdArgv(t *testing.T) {
	cmd := gitVersionCmd(t.Context())
	if got := cmd.Args; !slices.Equal(got, []string{"git", "version"}) {
		t.Errorf("argv = %q, want [git version]", got)
	}
	if cmd.Dir != "" {
		t.Errorf("dir = %q, want the daemon's own working directory", cmd.Dir)
	}
	// A nil Env hands git the daemon's environment unchanged.
	if cmd.Env != nil {
		t.Errorf("env = %q, want the daemon's environment unchanged", cmd.Env)
	}
}

// e27 is the E27 pattern: 33 segments. git matches a.txt at any depth, but the
// pattern opens nothing and sets no any-depth flag.
var e27 = strings.Repeat("**/", 32) + "a.txt\n"

// TestPlanIncludeScanMatchesReference feeds planIncludeScan the manifest and
// the listing of an interpretation scenario, byte for byte. The want batches
// are the pathspecs of the f6010b97 `git ls-files --exclude-from` calls. Linux
// and macOS gave the same batches.
func TestPlanIncludeScanMatchesReference(t *testing.T) {
	for _, c := range []struct {
		name, manifest, listing string
		files, dirs             [][]string
	}{
		{"I04_dstar_sub_build", "**/sub/build/\n", ".claude/\x00.claude/worktrees/\x00.envd/\x00build/\x00d/\x00other/\x00sub/build/\x00", nil, [][]string{{".envd", "sub/build"}}},                                    // item 4
		{"I04_lit_more", "**/build/a.txt\n", ".claude/\x00.claude/worktrees/\x00.envd/\x00build/\x00d/\x00other/\x00sub/build/\x00", nil, [][]string{{".envd", "build", "sub/build"}}},                                 // item 4
		{"I04_lit_dir_more", "**/build/x/\n", ".claude/\x00.claude/worktrees/\x00.envd/\x00build/\x00d/\x00other/\x00sub/build/\x00", nil, [][]string{{".envd", "build", "sub/build"}}},                                // item 4
		{"I04_dstar2_dir", "**/**/build/\n", ".claude/\x00.claude/worktrees/\x00.envd/\x00build/\x00d/\x00other/\x00sub/build/\x00", nil, [][]string{{".envd"}}},                                                       // item 4
		{"I04_dstar_glob_build", "**/*/build/\n", ".claude/\x00.claude/worktrees/\x00.envd/\x00build/\x00d/\x00other/\x00sub/build/\x00", nil, [][]string{{".envd"}}},                                                  // item 4
		{"I05_anch_dir", "/build/\n" + e27, ".claude/\x00.claude/worktrees/\x00.envd/\x00build/\x00", nil, [][]string{{"build"}}},                                                                                      // item 5
		{"I05_anch_glob_dir", "/b*/\n" + e27, ".claude/\x00.claude/worktrees/\x00.envd/\x00build/\x00", nil, [][]string{{"build"}}},                                                                                    // item 5
		{"I05_anch_lit", "/build\n" + e27, ".claude/\x00.claude/worktrees/\x00.envd/\x00build/\x00", nil, [][]string{{"build"}}},                                                                                       // item 5
		{"I05_anch_glob", "/b*\n" + e27, ".claude/\x00.claude/worktrees/\x00.envd/\x00build/\x00", nil, [][]string{{"build"}}},                                                                                         // item 5
		{"I05_anch_file", "/x.txt\n" + e27, ".claude/\x00.claude/worktrees/\x00.envd/\x00build/\x00", nil, nil},                                                                                                        // item 5
		{"I05_ctl_oneseg", "build/\n" + e27, ".claude/\x00.claude/worktrees/\x00.envd/\x00build/\x00", nil, [][]string{{".envd", "build"}}},                                                                            // item 5
		{"I05_ctl_e27_only", e27, ".claude/\x00.claude/worktrees/\x00.envd/\x00build/\x00", nil, nil},                                                                                                                  // item 5
		{"I09flag_tab", "\t\n" + e27, ".claude/\x00.claude/worktrees/\x00.envd/\x00", nil, nil},                                                                                                                        // item 9
		{"I09flag_sp_tab", " \t\n" + e27, ".claude/\x00.claude/worktrees/\x00.envd/\x00", nil, nil},                                                                                                                    // item 9
		{"I09flag_spaces_ctl", "   \n" + e27, ".claude/\x00.claude/worktrees/\x00.envd/\x00", nil, nil},                                                                                                                // item 9
		{"I11_ctl", "build/\n*.ig\nsu*\n", ".claude/\x00.claude/worktrees/\x00build/\x00other/\x00r.ig\x00sub/build/\x00", [][]string{{"r.ig"}}, [][]string{{"build", "sub/build"}}},                                   // item 10
		{"I16d_negation_star", "build/\n", ".claude/\x00.claude/worktrees/\x00build/a.txt\x00build/x/\x00", [][]string{{"build/a.txt"}}, [][]string{{"build/x"}}},                                                      // item 10
		{"I16g_magic_name_new", "*.ig\n", ".claude/\x00.claude/worktrees/\x000.ig\x00:!z.ig\x00a.ig\x00zz.ig\x00", [][]string{{"0.ig", ":!z.ig", "a.ig", "zz.ig"}}, nil},                                               // item 10
		{"I16i_magic_in_opened_dir", "build/\n", ".claude/\x00.claude/worktrees/\x00build/\x00", nil, [][]string{{"build"}}},                                                                                           // item 10
		{"I16b_nonignored_parent_opened", "su*\n", ".claude/\x00.claude/worktrees/\x00sub/build/\x00", nil, [][]string{{"sub/build"}}},                                                                                 // item 16
		{"I16f_global_excludes_in_opened_parent", "su*\n", ".claude/\x00.claude/worktrees/\x00sub/build/\x00sub/g.gl\x00", [][]string{{"sub/g.gl"}}, [][]string{{"sub/build"}}},                                        // item 16
		{"I14c_worktrees_unanchored", "*.dd\nworktrees/\n", ".claude/\x00.claude/worktrees/\x00top.dd\x00", [][]string{{"top.dd"}}, nil},                                                                               // item 14
		{"I14e_explicit", ".claude/worktrees/\n.claude/worktrees/stale/\n**/x.dd\n", ".claude/\x00.claude/worktrees/\x00top.dd\x00", [][]string{{"top.dd"}}, nil},                                                      // item 14
		{"I03d_adeep_starlog", "a/**/build/\n*.log\n", ".claude/\x00.claude/worktrees/\x00a/\x00a/other/\x00a/x/\x00a/x/build/\x00a/x/other/\x00", nil, [][]string{{"a", "a/other", "a/x", "a/x/build", "a/x/other"}}}, // item 16
	} {
		p := planIncludeScan([]byte(c.manifest), c.listing, "--exclude-from=/tmp/"+includeTempPrefix+"123456789")
		if p.fallback || !reflect.DeepEqual(p.fileBatches, c.files) || !reflect.DeepEqual(p.dirBatches, c.dirs) {
			t.Errorf("%s: files %q dirs %q, want files %q dirs %q (f6010b97)", c.name, p.fileBatches, p.dirBatches, c.files, c.dirs)
		}
	}
}

// TestIncludeTempPrefixLength pins the length of the temp name prefix. The
// measured f6010b97 prefix is 27 bytes. The `--exclude-from` argument takes its
// length from the batch budget. A rename to another length moves the batch
// split point, so this test fails first.
func TestIncludeTempPrefixLength(t *testing.T) {
	if len(includeTempPrefix) != 27 {
		t.Errorf("len(%q) = %d, want 27 (f6010b97 argv)", includeTempPrefix, len(includeTempPrefix))
	}
	checkIncludeTempName(t, includeTempPrefix+"123456789")
}

// checkIncludeTempName checks that name is includeTempPrefix plus a decimal
// suffix of 1 to 10 digits.
func checkIncludeTempName(t *testing.T, name string) {
	t.Helper()
	suffix, ok := strings.CutPrefix(name, includeTempPrefix)
	if !ok || len(suffix) < 1 || len(suffix) > 10 || strings.Trim(suffix, "0123456789") != "" {
		t.Errorf("temp name %q, want %q plus 1 to 10 decimal digits", name, includeTempPrefix)
	}
}

// TestPlanIncludeScanCounts covers what counts toward the 256 cap (items 7
// and 9) and the dot-directory places (item 12). One scenario fixture has 256
// ignored directories d001 to d256, named one per line. The other has 128
// ignored dot directories and `*.dd`. The want counts are the f6010b97 batch sizes. The
// case explicit_root_claude_takes_no_place is not measured. Its want list is
// derived from the item-12 rule for an explicit opener.
func TestPlanIncludeScanCounts(t *testing.T) {
	dirList := func(names []string) string {
		l := ".claude/\x00.claude/worktrees/\x00"
		for _, n := range names {
			l += n + "/\x00"
		}
		return l
	}
	d256 := seqf(256, "d%03d")
	dot128 := seqf(128, ".d%03d")
	opened := func(first string) string { return first + "\n" + strings.Join(d256, "/\n") + "/\n" }
	for _, c := range []struct {
		name, manifest, listing string
		want                    []string
	}{
		{"I07_ctl_none", strings.Join(d256, "/\n") + "/\n", dirList(d256), d256},
		{"I07_dslash", opened("//"), dirList(d256), d256},
		{"I07_x_dslash", opened("x//"), dirList(d256), d256[:255]},
		{"I07_a_dslash_b", opened("a//b"), dirList(d256), d256[:255]},
		{"I07_dslash_x", opened("//x"), dirList(d256), d256[:255]},
		{"I07_dslash_mid_dir", opened("a//b/"), dirList(d256), d256[:255]},
		{"I09_tab", opened("\t"), dirList(d256), d256},
		{"I09_tab_sp", opened("\t "), dirList(d256), d256},
		{"I09_sp_tab", opened(" \t"), dirList(d256), d256},
		{"I09_spaces_ctl", opened("   "), dirList(d256), d256},
		{"I09_esc_space", opened(`\ `), dirList(d256), d256[:255]},
		{"I12b_explicit_dot_takes_no_place", ".d001/\n*.dd\n", dirList(dot128), dot128},
		{"I12c_explicit_last_dot", ".d128/\n*.dd\n", dirList(dot128), dot128},
		{"explicit_root_claude_takes_no_place", ".claude/\n*.dd\n", dirList(dot128), dot128},
		{"F01_dotcap_128", "*.dd\n", dirList(dot128), dot128[:127]},
	} {
		p := planIncludeScan([]byte(c.manifest), c.listing, "--exclude-from=/tmp/x")
		if len(p.dirBatches) != 1 || !slices.Equal(p.dirBatches[0], c.want) {
			var got []string
			for _, b := range p.dirBatches {
				got = append(got, b...)
			}
			t.Errorf("%s: opens %d dirs, want %d (f6010b97)", c.name, len(got), len(c.want))
		}
	}
}

// Fixtures of the interpretation matrix
// (scratch/f6010b97/slice4-interpretations.md). The want lists are the
// f6010b97 file sets, the same on Linux and macOS.
var (
	d4Ignore = lines("build/", "other/", ".envd/", "d/")
	d4Files  = []string{"build/a.txt", "build/x/a.txt", "sub/build/a.txt", "other/o.log", "other/a.txt",
		".envd/build/a.txt", "d/a.txt", "d/e/a.txt"}
	d5Ignore = lines(".envd/", "build/")
	d5Files  = []string{".envd/a.txt", "build/a.txt"}
	// errTree is the err_tree fixture: `sub/u.ig` is not ignored, and `su*`
	// opens `sub/build` because the path of the listing entry starts with `su`.
	errTree = includeCase{ignore: lines("build/", "other/", "*.ig", "!sub/u.ig"),
		files:    []string{"build/a.txt", "other/o.ig", "r.ig", "sub/build/c.ig", "sub/u.ig"},
		manifest: "build/\n*.ig\nsu*\n"}
	errTreeNew = []string{"build/a.txt", "r.ig", "sub/build/c.ig"}
	errTreeOld = []string{"build/a.txt", "other/o.ig", "r.ig", "sub/build/c.ig"}
)

// TestIncludeInterpretationScenarios runs the interpretation scenarios end to
// end with the real git: items 4, 5, 7, 9, 12 and 16.
func TestIncludeInterpretationScenarios(t *testing.T) {
	d4 := func(name, manifest string, want ...string) includeCase {
		return includeCase{name: name, ignore: d4Ignore, tracked: []string{"sub/t.md"}, files: d4Files,
			manifest: manifest, want: want}
	}
	d5 := func(name, first string, want ...string) includeCase {
		return includeCase{name: name, ignore: d5Ignore, files: d5Files, manifest: first + "\n" + e27, want: want}
	}
	many := func(name, first string, n int) includeCase {
		dirs := seqf(256, "d%03d/")
		return includeCase{name: name, ignore: lines("/d*/"), files: seqf(256, "d%03d/f.txt"),
			manifest: first + "\n" + strings.Join(dirs, "\n") + "\n", want: seqf(n, "d%03d/f.txt")}
	}
	dots := func(name, manifest string) includeCase {
		c := includeCase{name: name, manifest: manifest}
		for _, d := range seqf(128, ".d%03d") {
			c.ignore += "/" + d + "/\n"
			c.files = append(c.files, d+"/f.dd")
		}
		c.want = c.files
		return c
	}
	flag := func(name, first string) includeCase {
		return includeCase{name: name, ignore: dotdIgnore, files: []string{".envd/a.txt"}, manifest: first + "\n" + e27}
	}
	checkIncludeCases(t, []includeCase{
		d4("I04_dstar_sub_build", "**/sub/build/\n", "sub/build/a.txt"),
		d4("I04_lit_more", "**/build/a.txt\n", "build/a.txt", "sub/build/a.txt", ".envd/build/a.txt"),
		d4("I04_dstar2_dir", "**/**/build/\n", ".envd/build/a.txt"),
		d5("I05_ctl_oneseg", "build/", ".envd/a.txt", "build/a.txt"),
		d5("I05_anch_dir", "/build/", "build/a.txt"),
		d5("I05_anch_glob_dir", "/b*/", "build/a.txt"),
		d5("I05_anch_lit", "/build", "build/a.txt"),
		d5("I05_anch_glob", "/b*", "build/a.txt"),
		d5("I05_anch_file", "/x.txt"),
		many("I07_dslash", "//", 256),
		many("I07_x_dslash", "x//", 255),
		many("I09_tab", "\t", 256),
		many("I09_sp_tab", " \t", 256),
		many("I09_esc_space", `\ `, 255),
		flag("I09flag_tab", "\t"),
		flag("I09flag_sp_tab", " \t"),
		dots("I12b_explicit_dot_takes_no_place", ".d001/\n*.dd\n"),
		{name: "I16b_nonignored_parent_opened", ignore: lines("build/"),
			files: []string{"sub/build/c.txt", "sub/u.txt"}, manifest: "su*\n", want: []string{"sub/build/c.txt"}},
		{name: "I16d_negation_star", ignore: lines("build/*", "!build/keep.txt", "build/*/"),
			files: []string{"build/a.txt", "build/keep.txt", "build/x/b.txt"}, manifest: "build/\n",
			want: []string{"build/a.txt", "build/x/b.txt"}},
		{name: "I11_ctl", ignore: errTree.ignore, files: errTree.files, manifest: errTree.manifest, want: errTreeNew},
	})
}

// yTreeDirs are the directory entries of the listing of the yTree fixture of
// the Windows probe. `build/` and `.bx/` are ignored. The fixture holds
// `build/a.txt`, `sub/build/b.txt`, `a/x/build/c.txt` and `.bx/d.txt`.
var yTreeDirs = []string{".bx", "a", "a/x", "a/x/build", "build", "sub", "sub/build"}

// TestIncludeAnchoredOneSegment covers the rule for a leading `/` on one
// segment. The want lists are the f6010b97 directory batches of O2_anch and
// O_anch, measured on a Windows VM. The Linux and macOS batches with a nested
// `build` are not measured.
func TestIncludeAnchoredOneSegment(t *testing.T) {
	for manifest, want := range map[string][]string{
		"/build/\n*.txt\n": {".bx", "build"},                           // O2_anch
		"/build/\n":        {"build"},                                  // O_anch
		"/b*/\n":           {"build"},                                  // Y22: the glob form
		"build/\n":         {".bx", "a/x/build", "build", "sub/build"}, // control
	} {
		if got := openIncludeDirs(parseIncludePlan([]byte(manifest)), yTreeDirs); !slices.Equal(got, want) {
			t.Errorf("manifest %q opens %v, want %v (f6010b97)", manifest, got, want)
		}
	}
}

// spTreeDirs are the directory entries of the listing of the sptree fixture of
// the macOS probe, in listing order. The root holds 17 ignored directories. The
// directory `a` holds a tracked file and the ignored directories `a/b`, `a/ b`
// and `a/c`.
var spTreeDirs = []string{" b", "a  b", "a b", "a*", "a/ b", "a/b", "a/c", "aXb", `a\ b`, `a\`, `a\b`, "a_b",
	"ab", "ac", "b", "x  y", "x y", `x\ y`, `x\y`, "xy"}

// bsTreeDirs are the directory entries of the listing of the bstree fixture of
// the macOS probe, in listing order. They are yTreeDirs plus five ignored
// directories with a literal `\` in the name or none.
var bsTreeDirs = []string{".bx", "a", "a/x", "a/x/build", `a\x\build`, "axbuild", "build", `build\`, "sub",
	"sub/build", `sub\build`, "subbuild"}

// lcTreeDirs, mixTreeDirs, anTreeDirs and pTreeDirs are the directory entries of
// four Linux probe fixtures, in listing order. The fixtures are lctree,
// mixtree, antree and ptree. In ptree, `a`, `sub`, `subx` and `suy` hold a
// tracked file, so they are not listed.
var (
	lcTreeDirs  = []string{"aXb", "a_b", "ab", "abc", "ac", "bu", "build", "bx", "zz"}
	mixTreeDirs = []string{"Ab", "BUILD2", "Build", "ab", "bUx", "build"}
	anTreeDirs  = []string{"aXb", "a_b", "ab", "abcd", "ac", "build", "bx", "cb", "sub/aXb", "sub/a_b", "sub/ab",
		"sub/build", "sub/bx"}
	pTreeDirs = []string{"a/b1c", "a/bc", "a/bxc", "a/bzz", "ab", "sub/build", "sub/bx", "sub/cx", "sub/x", `sub\`,
		"subq", "subx/build", "suy/build"}
)

// cTreeDirs, dTreeDirs, dTree2Dirs, sTreeDirs and gTreeDirs are the directory
// entries of five fixtures of the second Linux probe, in listing order. The
// fixtures are ctree, dtree, dtree2, stree and gtree. Each listing holds `.claude` and
// `.claude/worktrees` too.
var (
	cTreeDirs = []string{".claude", ".claude/worktrees", "a/q/x", "a/q/y", "a/zz/x", `a/zz\/x`, "ab", "q/x", "q/y",
		"sub/x", `sub/x\`, "sub/xa", "sub/y", `sub\/x`, `sub\/y`, "x", "zz", `zz\/x`, `zz\/y`}
	dTreeDirs  = []string{".claude", ".claude/worktrees", "a/b", "a/c", "ab", "sub/a/b", "sub/x", "x"}
	dTree2Dirs = []string{".claude", ".claude/worktrees", "a", "ab", "sub/a/b", "sub/x", "x"}
	sTreeDirs  = []string{".b", ".bx", ".claude", ".claude/worktrees", "ab", "bq", "sub/.bx", "sub/ab", "sub/x", "x"}
	gTreeDirs  = []string{".bx", ".claude", ".claude/worktrees", "a/x/build", "build", "bx", "sub/bq", "sub/build"}
)

// TestIncludePrefixOpening covers the rule for one glob segment: it opens each
// listed directory whose path starts with the literal prefix of the segment.
// The prefix ends before the first `*`, `?`, `[` or `\`, and case does not
// matter. It also covers a leading `/`, a `//` and a lone `\` in a longer
// pattern. The want lists are the f6010b97 directory batches. The O2 and O3
// rows were measured on Windows and macOS VMs. The rows whose names start with
// `L` were measured on a Linux VM, and the other rows on a macOS VM. A row with
// a `*.txt` line opens the dot directory `.bx` too.
func TestIncludePrefixOpening(t *testing.T) {
	aAll := []string{"a  b", "a b", "a*", "a/ b", "a/b", "a/c", "aXb", `a\ b`, `a\`, `a\b`, "a_b", "ab", "ac"}
	xAll := []string{"x  y", "x y", `x\ y`, `x\y`, "xy"}
	subAll := []string{".bx", "sub", "sub/build", `sub\build`, "subbuild"}
	weird := []string{"we[i]rd", "weird"}
	lcA := []string{"aXb", "a_b", "ab", "abc", "ac"}
	mixB := []string{"BUILD2", "Build", "bUx", "build"}
	anA := []string{"aXb", "a_b", "ab", "abcd", "ac"}
	pSub := []string{"sub/build", "sub/bx", "sub/cx", "sub/x", `sub\`, "subq", "subx/build"}
	cAnyX := []string{"ab", "q/x", "sub/x", `sub\/x`, "x", "zz", `zz\/x`}
	cSubAny := []string{"sub/x", `sub/x\`, "sub/xa", "sub/y"}
	sAll := []string{".b", ".bx", "ab", "bq", "sub/.bx", "sub/ab", "sub/x", "x"}
	for _, c := range []struct {
		name, manifest string
		dirs, want     []string
	}{
		{"a_bs_sp_b", "a\\ b/\n*.txt\n", spTreeDirs, aAll},
		{"a_bs_sp_b_noslash", "a\\ b\n*.txt\n", spTreeDirs, aAll},
		{"a_bs_b", "a\\b/\n*.txt\n", spTreeDirs, aAll},
		{"a_bsbs_b", "a\\\\b/\n*.txt\n", spTreeDirs, aAll},
		{"a_bsbs_sp_b", "a\\\\ b/\n*.txt\n", spTreeDirs, aAll},
		{"a_bs_star", "a\\*/\n*.txt\n", spTreeDirs, aAll},
		{"a_q_bs_q", "a?\\q/\n*.txt\n", spTreeDirs, aAll},
		{"ab_bs_q", "ab\\q/\n*.txt\n", spTreeDirs, []string{"ab"}},
		{"AB_bs_q", "AB\\q/\n*.txt\n", spTreeDirs, []string{"ab"}},
		{"a__bs_q", "a_\\q/\n*.txt\n", spTreeDirs, []string{"a_b"}},
		{"x_bs_sp_y", "x\\ y/\n*.txt\n", spTreeDirs, xAll},
		{"x_bs_y", "x\\y/\n*.txt\n", spTreeDirs, xAll},
		{"x_bs_q", "x\\q/\n*.txt\n", spTreeDirs, xAll},
		{"x_sp_bs_q", "x \\q/\n*.txt\n", spTreeDirs, []string{"x  y", "x y"}},
		{"bs_sp_b", "\\ b/\n*.txt\n", spTreeDirs, nil},
		{"a_star_ctl", "a*/\n*.txt\n", spTreeDirs, aAll},
		{"ab_star_ctl", "ab*/\n*.txt\n", spTreeDirs, []string{"ab"}},
		{"a_q_b", "a?b/\n*.txt\n", spTreeDirs, aAll},
		{"a_class_b", "a[_]b/\n*.txt\n", spTreeDirs, aAll},
		{"a_sp_b_ctl", "a b/\n*.txt\n", spTreeDirs, []string{"a b"}},
		{"x_sp_y_ctl", "x y/\n*.txt\n", spTreeDirs, []string{"x y"}},
		{"ab_ctl", "ab/\n*.txt\n", spTreeDirs, []string{"ab"}},
		{"a_b_ctl", "a/b/\n*.txt\n", spTreeDirs, []string{"a/b"}},
		{"a_sp_b_multi_ctl", "a/ b/\n*.txt\n", spTreeDirs, []string{"a/ b"}},
		{"bui_bs_q", "bui\\q/\n", yTreeDirs, []string{".bx", "build"}},
		{"O2_bslash_sep", "sub\\build/\n*.txt\n", yTreeDirs, []string{".bx", "sub", "sub/build"}},
		{"O3_sub_esc_b_star", "sub\\\\build/\n*.txt\n", yTreeDirs, []string{".bx", "sub", "sub/build"}},
		{"O2_bslash_trail", "build\\\n*.txt\n", yTreeDirs, []string{".bx", "build"}},
		{"O2_bslash_glob", "sub\\b*/\n*.txt\n", yTreeDirs, []string{".bx", "sub", "sub/build"}},
		{"O2_bslash_file", "build\\a.txt\n*.txt\n", yTreeDirs, []string{".bx", "build"}},
		{"O2_bslash_axbuild", "a\\x\\build/\n*.txt\n", yTreeDirs, []string{".bx", "a", "a/x", "a/x/build"}},
		{"O2_bslash_lead", "\\build/\n*.txt\n", yTreeDirs, []string{".bx"}},
		{"O2_bslash_ds", "**\\build/\n*.txt\n", yTreeDirs, []string{".bx"}},
		{"O3_bu_esc_ild", "bu\\ild/\n*.txt\n", yTreeDirs, []string{".bx", "build"}},
		{"Y35", "b\\*/\n", yTreeDirs, []string{".bx", "build"}},
		{"X17b", "we\\[i\\]rd/\n", weird, weird},
		{"X18", "we[i]rd/\n", []string{"weird"}, []string{"weird"}},
		{"bs_sub_build", "sub\\build/\n*.txt\n", bsTreeDirs, subAll},
		{"bs_sub_bsbs_build", "sub\\\\build/\n*.txt\n", bsTreeDirs, subAll},
		{"bs_build_trail", "build\\\n*.txt\n", bsTreeDirs, []string{".bx", "build", `build\`}},
		{"bs_lead", "\\build/\n*.txt\n", bsTreeDirs, []string{".bx"}},
		{"bs_ds", "**\\build/\n*.txt\n", bsTreeDirs, []string{".bx"}},
		{"bs_sub_b_star", "sub\\b*/\n*.txt\n", bsTreeDirs, subAll},
		{"bs_build_file", "build\\a.txt\n*.txt\n", bsTreeDirs, []string{".bx", "build", `build\`}},
		{"bs_axbuild", "a\\x\\build/\n*.txt\n", bsTreeDirs,
			[]string{".bx", "a", "a/x", "a/x/build", `a\x\build`, "axbuild"}},
		{"bs_bu_ild", "bu\\ild/\n*.txt\n", bsTreeDirs, []string{".bx", "build", `build\`}},
		{"bs_su_bbuild", "su\\bbuild/\n*.txt\n", bsTreeDirs, subAll},
		{"bs_s_ub_build", "s\\ub\\build/\n*.txt\n", bsTreeDirs, subAll},
		{"bs_sub_build_ctl", "sub/build/\n*.txt\n", bsTreeDirs, []string{".bx", "sub", "sub/build"}},
		{"bs_build_ctl", "build/\n*.txt\n", bsTreeDirs, []string{".bx", "a/x/build", "build", "sub/build"}},
		{"bs_axbuild_ctl", "a/x/build/\n*.txt\n", bsTreeDirs, []string{".bx", "a", "a/x", "a/x/build"}},
		{"bs_subbuild_ctl", "subbuild/\n*.txt\n", bsTreeDirs, []string{".bx", "subbuild"}},
		{"multi_sub_b_bs_q", "sub/b\\q/\n", yTreeDirs, []string{"sub"}},
		{"multi_a_b_bs_q", "a/b\\q/\n", spTreeDirs, nil},
		{"QS_sep_q", "a/\\q/\n*.txt\n", spTreeDirs, nil},
		{"QY_bx_q", ".b\\q/\n*.txt\n", yTreeDirs, []string{".bx"}},
		{"QY_sub_b_q", "sub/b\\q/\n*.txt\n", yTreeDirs, []string{".bx", "sub"}},
		{"QY_star_b_q", "*/b\\q/\n*.txt\n", yTreeDirs, []string{".bx", "a", "build", "sub"}},
		// Q1 on Linux: case folds on ext4 too.
		{"L1c_AB_q", "AB\\q/\n*.txt\n", lcTreeDirs, []string{"ab", "abc"}},
		{"L1c_AB_q_alone", "AB\\q/\n", lcTreeDirs, []string{"ab", "abc"}},
		{"L1c_A_q", "A\\q/\n*.txt\n", lcTreeDirs, lcA},
		{"L1c_B_star", "B*/\n*.txt\n", lcTreeDirs, []string{"bu", "build", "bx"}},
		{"L1c_BU_qm_ld", "BU?ld/\n*.txt\n", lcTreeDirs, []string{"bu", "build"}},
		{"L1c_BUILD_lit", "BUILD/\n*.txt\n", lcTreeDirs, []string{"build"}},
		{"L1c_ctl_ab_q", "ab\\q/\n*.txt\n", lcTreeDirs, []string{"ab", "abc"}},
		{"L1c_ctl_a_q", "a\\q/\n*.txt\n", lcTreeDirs, lcA},
		{"L1c_ctl_b_star", "b*/\n*.txt\n", lcTreeDirs, []string{"bu", "build", "bx"}},
		{"L1c_ctl_bu_qm_ld", "bu?ld/\n*.txt\n", lcTreeDirs, []string{"bu", "build"}},
		{"L1c_ctl_build_lit", "build/\n*.txt\n", lcTreeDirs, []string{"build"}},
		{"L1c_txt_ctl", "*.txt\n", lcTreeDirs, nil},
		{"L1m_A_q", "A\\q/\n*.txt\n", mixTreeDirs, []string{"Ab", "ab"}},
		{"L1m_B_star", "B*/\n*.txt\n", mixTreeDirs, mixB},
		{"L1m_b_star", "b*/\n*.txt\n", mixTreeDirs, mixB},
		{"L1m_BU_qm_ld", "BU?ld/\n*.txt\n", mixTreeDirs, mixB},
		{"L1m_Bu_qm_ld", "Bu?ld/\n*.txt\n", mixTreeDirs, mixB},
		{"L1m_bu_qm_ld", "bu?ld/\n*.txt\n", mixTreeDirs, mixB},
		{"L1m_Bu_q", "Bu\\q/\n*.txt\n", mixTreeDirs, mixB},
		{"L1m_bu_q", "bu\\q/\n*.txt\n", mixTreeDirs, mixB},
		{"L1m_Build_lit", "Build/\n*.txt\n", mixTreeDirs, []string{"Build", "build"}},
		{"L1m_build_lit", "build/\n*.txt\n", mixTreeDirs, []string{"Build", "build"}},
		{"L1m_BUILD_lit", "BUILD/\n*.txt\n", mixTreeDirs, []string{"Build", "build"}},
		{"L1m_txt_ctl", "*.txt\n", mixTreeDirs, nil},
		{"L5y_SUB_BUILD", "SUB/BUILD/\n*.txt\n", yTreeDirs, []string{".bx", "sub", "sub/build"}},
		{"L5y_anch_BUILD", "/BUILD/\n*.txt\n", yTreeDirs, []string{".bx", "build"}},
		{"L5y_Sub_q_star", "SUB\\q/\n*.txt\n", yTreeDirs, []string{".bx", "sub", "sub/build"}},
		{"L5a_anch_AB", "/AB/\n*.txt\n", anTreeDirs, []string{"ab"}},
		{"L5a_anch_Bstar", "/B*/\n*.txt\n", anTreeDirs, []string{"build", "bx"}},
		// Q2 on Linux: one segment after a leading `/` is a glob over the first
		// segment. It has no prefix rule, and a `\` escapes.
		{"L2_anch_aqmb", "/a?b/\n*.txt\n", anTreeDirs, []string{"aXb", "a_b"}},
		{"L2_anch_aqmb_bare", "/a?b\n*.txt\n", anTreeDirs, []string{"aXb", "a_b"}},
		{"L2_anch_aqmb_alone", "/a?b/\n", anTreeDirs, []string{"aXb", "a_b"}},
		{"L2_anch_acls", "/a[_]b/\n*.txt\n", anTreeDirs, []string{"a_b"}},
		{"L2_anch_esc_q", "/ab\\q/\n*.txt\n", anTreeDirs, nil},
		{"L5a_anch_AqmB", "/A?B/\n*.txt\n", anTreeDirs, []string{"aXb", "a_b"}},
		{"L2_anch_bstar", "/b*/\n*.txt\n", anTreeDirs, []string{"build", "bx"}},
		{"L2_anch_lit_ab", "/ab/\n*.txt\n", anTreeDirs, []string{"ab"}},
		{"L2_anch_lit_build", "/build/\n*.txt\n", anTreeDirs, []string{"build"}},
		{"L2_ctl_aqmb", "a?b/\n*.txt\n", anTreeDirs, anA},
		{"L2_ctl_acls", "a[_]b/\n*.txt\n", anTreeDirs, anA},
		{"L2_ctl_bstar", "b*/\n*.txt\n", anTreeDirs, []string{"build", "bx"}},
		{"L2_ctl_lit_ab", "ab/\n*.txt\n", anTreeDirs, []string{"ab", "sub/ab"}},
		{"L2_txt_ctl", "*.txt\n", anTreeDirs, nil},
		// Q3 on Linux: a `//` keeps an empty segment that matches no name.
		{"L3_ds_file_txt", "build//a.txt\n*.txt\n", yTreeDirs, []string{".bx", "build"}},
		{"L3_ds_file_alone", "build//a.txt\n", yTreeDirs, []string{"build"}},
		{"L3_ds_dir_txt", "build//\n*.txt\n", yTreeDirs, []string{".bx", "build"}},
		{"L3_ds_dir_alone", "build//\n", yTreeDirs, []string{"build"}},
		{"L5y_BUILD_ds", "BUILD//\n*.txt\n", yTreeDirs, []string{".bx", "build"}},
		{"L3_ctl_file_txt", "build/a.txt\n*.txt\n", yTreeDirs, []string{".bx", "build"}},
		{"L3_ctl_file_alone", "build/a.txt\n", yTreeDirs, []string{"build"}},
		// Q4 on Linux: a segment that ends in a lone `\` matches any name.
		{"L4_a_bqmc", "a/b?c/\n*.txt\n", pTreeDirs, []string{"a/b1c", "a/bxc"}},
		{"L4_a_bstar", "a/b*/\n*.txt\n", pTreeDirs, []string{"a/b1c", "a/bc", "a/bxc", "a/bzz"}},
		{"L4_sub_bstar", "sub/b*/\n*.txt\n", pTreeDirs, []string{"sub/build", "sub/bx"}},
		{"L4_sustar_build", "su*/build/\n*.txt\n", pTreeDirs,
			[]string{"sub/build", `sub\`, "subq", "subx/build", "suy/build"}},
		{"L4_a_b_q", "a/b\\q/\n*.txt\n", pTreeDirs, nil},
		{"L4_sub_q", "sub/\\q/\n*.txt\n", pTreeDirs, nil},
		{"L4_sub_bs_q", "sub\\q/\n*.txt\n", pTreeDirs, pSub},
		{"L4_sstar", "s*/\n*.txt\n", pTreeDirs, append(slices.Clone(pSub), "suy/build")},
		{"L5p_sub_trail_bs", "sub\\/\n*.txt\n", pTreeDirs, pSub},
		{"L4_sub_bs_sl_x", "sub\\/x/\n*.txt\n", pTreeDirs, []string{"ab", "sub/x", `sub\`, "subq"}},
		{"L4_sub_bs_sl_x_alone", "sub\\/x/\n", pTreeDirs, []string{"ab", "sub/x", `sub\`, "subq"}},
		{"L5p_zz_bs_sl_x", "zz\\/x/\n*.txt\n", pTreeDirs, []string{"ab", "sub/x", `sub\`, "subq"}},
		{"L5p_zz_bs_sl_bstar", "zz\\/b*/\n*.txt\n", pTreeDirs, []string{"a/b1c", "a/bc", "a/bxc", "a/bzz", "ab",
			"sub/build", "sub/bx", `sub\`, "subq", "subx/build", "suy/build"}},
		{"L5p_a_bs_sl_b1c", "a\\/b1c/\n*.txt\n", pTreeDirs, []string{"a/b1c", "ab", `sub\`, "subq"}},
		{"L5p_sub_bs_sl_nomatch", "sub\\/nomatch/\n*.txt\n", pTreeDirs, []string{"ab", `sub\`, "subq"}},
		{"L4_ctl_a_b1c", "a/b1c/\n*.txt\n", pTreeDirs, []string{"a/b1c"}},
		{"L4_ctl_sub_build", "sub/build/\n*.txt\n", pTreeDirs, []string{"sub/build"}},
		{"L4_ctl_sub_x", "sub/x/\n*.txt\n", pTreeDirs, []string{"sub/x"}},
		{"L5p_ctl_zz_x", "zz/x/\n*.txt\n", pTreeDirs, nil},
		{"L4_txt_ctl", "*.txt\n", pTreeDirs, nil},
		// Second Linux probe, Q1: an even run of trailing `\` is a literal `\`.
		// A run of three acts like a run of one and matches any name.
		{"LC1_zz_bs2_x_a", `zz\\/x/` + "\n", cTreeDirs, []string{`zz\/x`}},
		{"LC1_zz_bs2_x_t", `zz\\/x/` + "\n*.txt\n", cTreeDirs, []string{`zz\/x`}},
		{"LC1_sub_bs2_x_a", `sub\\/x/` + "\n", cTreeDirs, []string{`sub\/x`}},
		{"LC1_sub_bs2_x_t", `sub\\/x/` + "\n*.txt\n", cTreeDirs, []string{`sub\/x`}},
		{"LC1_zz_bs3_x_a", `zz\\\/x/` + "\n", cTreeDirs, cAnyX},
		{"LC1_zz_bs3_x_t", `zz\\\/x/` + "\n*.txt\n", cTreeDirs, cAnyX},
		{"LC1_sub_bs3_x_a", `sub\\\/x/` + "\n", cTreeDirs, cAnyX},
		{"LC1_sub_bs3_x_t", `sub\\\/x/` + "\n*.txt\n", cTreeDirs, cAnyX},
		{"LC1_ctl_zz_bs1_x_a", `zz\/x/` + "\n", cTreeDirs, cAnyX},
		{"LC1_ctl_zz_x_a", "zz/x/\n", cTreeDirs, []string{"zz"}},
		// Q2: a lone trailing `\` in a middle or last segment matches any name.
		{"LC2_a_zz_bs_x_a", `a/zz\/x/` + "\n", cTreeDirs, []string{"a/q/x", "a/zz/x", `a/zz\/x`}},
		{"LC2_a_zz_bs_x_t", `a/zz\/x/` + "\n*.txt\n", cTreeDirs, []string{"a/q/x", "a/zz/x", `a/zz\/x`}},
		{"LC2_sub_x_bs_a", `sub/x\` + "\n", cTreeDirs, cSubAny},
		{"LC2_sub_x_bs_t", `sub/x\` + "\n*.txt\n", cTreeDirs, cSubAny},
		{"LC2_sub_x_bs_sl_a", `sub/x\/` + "\n", cTreeDirs, cSubAny},
		{"LC2_sub_x_bs_sl_t", `sub/x\/` + "\n*.txt\n", cTreeDirs, cSubAny},
		{"LC2_ctl_a_zz_x_a", "a/zz/x/\n", cTreeDirs, []string{"a/zz/x"}},
		{"LC2_ctl_sub_x_a", "sub/x/\n", cTreeDirs, []string{"sub/x"}},
		// Q3: a lone `\` behind `**/` opens nothing.
		{"LC3_ss_zz_bs_x_a", `**/zz\/x/` + "\n", cTreeDirs, nil},
		{"LC3_ss_zz_bs_x_t", `**/zz\/x/` + "\n*.txt\n", cTreeDirs, nil},
		{"LC3_ctl_ss_zz_x_a", "**/zz/x/\n", cTreeDirs, []string{"a/zz/x", "zz"}},
		// Q4: an empty segment matches no name.
		{"LC4d_dsl_x_a", "//x\n", dTreeDirs, nil},
		{"LC4d_dsl_x_t", "//x\n*.txt\n", dTreeDirs, nil},
		{"LC4d_dsl_x_sl_a", "//x/\n", dTreeDirs, nil},
		{"LC4d_dsl_x_sl_t", "//x/\n*.txt\n", dTreeDirs, nil},
		{"LC4d_a_ds_b_a", "a//b\n", dTreeDirs, nil},
		{"LC4d_a_ds_b_t", "a//b\n*.txt\n", dTreeDirs, nil},
		{"LC4d_a_ds_b_sl_a", "a//b/\n", dTreeDirs, nil},
		{"LC4d_a_ds_b_sl_t", "a//b/\n*.txt\n", dTreeDirs, nil},
		{"LC4d_ctl_anch_x_a", "/x/\n", dTreeDirs, []string{"x"}},
		{"LC4d_ctl_a_b_a", "a/b/\n", dTreeDirs, []string{"a/b"}},
		{"LC4w_a_ds_b_a", "a//b\n", dTree2Dirs, []string{"a"}},
		{"LC4w_a_ds_b_t", "a//b\n*.txt\n", dTree2Dirs, []string{"a"}},
		{"LC4w_a_ds_b_sl_a", "a//b/\n", dTree2Dirs, []string{"a"}},
		{"LC4w_a_ds_b_sl_t", "a//b/\n*.txt\n", dTree2Dirs, []string{"a"}},
		{"LC4w_ctl_a_b_a", "a/b/\n", dTree2Dirs, []string{"a"}},
		// Q5: an anchored glob matches the first segment of each listed entry.
		{"LC5_anch_star_sl_a", "/*/\n", sTreeDirs, sAll},
		{"LC5_anch_star_sl_t", "/*/\n*.txt\n", sTreeDirs, sAll},
		{"LC5_anch_star_a", "/*\n", sTreeDirs, sAll},
		{"LC5_anch_star_t", "/*\n*.txt\n", sTreeDirs, sAll},
		{"LC5_anch_qm_sl_a", "/?/\n", sTreeDirs, []string{"x"}},
		{"LC5_anch_qm_sl_t", "/?/\n*.txt\n", sTreeDirs, []string{".b", ".bx", "sub/.bx", "x"}},
		{"LC5_anch_dotb_star_a", "/.b*/\n", sTreeDirs, []string{".b", ".bx"}},
		{"LC5_anch_dotb_star_t", "/.b*/\n*.txt\n", sTreeDirs, []string{".b", ".bx", "sub/.bx"}},
		{"LC5_ctl_anch_dotbx_a", "/.bx/\n", sTreeDirs, []string{".bx"}},
		// Q6: a `//` in a glob line keeps the empty segment.
		{"LC6_bstar_ds_atxt_a", "b*//a.txt\n", gTreeDirs, []string{"build", "bx"}},
		{"LC6_bstar_ds_atxt_t", "b*//a.txt\n*.txt\n", gTreeDirs, []string{".bx", "build", "bx"}},
		{"LC6_star_ds_a", "*//\n", gTreeDirs, []string{".bx", "build", "bx"}},
		{"LC6_star_ds_t", "*//\n*.txt\n", gTreeDirs, []string{".bx", "build", "bx"}},
		{"LC6_ctl_build_ds_a", "build//\n", gTreeDirs, []string{"build"}},
	} {
		got := openIncludeDirs(parseIncludePlan([]byte(c.manifest)), c.dirs)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: manifest %q opens %v, want %v (f6010b97)", c.name, c.manifest, got, c.want)
		}
	}
}

// TestClaudeDirPassArgs covers the argv of the `.claude/` pass. f6010b97 names
// each child of `.claude/` except `worktrees`, and runs no call when no other
// child exists (Linux, macOS and Windows logs). The order of the directory read
// is not fixed, so the test compares sets.
func TestClaudeDirPassArgs(t *testing.T) {
	prefix := []string{"--literal-pathspecs", "ls-files", "--others", "--ignored", "--exclude-standard", "-z", "--"}
	for _, c := range []struct {
		name  string
		files []string
		want  []string
	}{
		{"no_claude_dir", nil, nil},
		{"I14a_only_worktrees", []string{".claude/worktrees/stale/x.dd"}, nil},
		{"I14b_claude_whole_ignored", []string{".claude/worktrees/stale/x.dd", ".claude/keep.dd"},
			[]string{".claude/keep.dd"}},
		{"Cl_nomanifest", []string{".claude/settings.local.json", ".claude/checkpoints/x.json", ".claude/untr.json"},
			[]string{".claude/checkpoints", ".claude/settings.local.json", ".claude/untr.json"}},
		{"I15b_nearname", []string{".claude/worktreesX/a.dd", ".claude/worktrees/stale/x.dd", ".claude/k.dd"},
			[]string{".claude/k.dd", ".claude/worktreesX"}},
	} {
		repo := t.TempDir()
		for _, f := range c.files {
			writeFile(t, filepath.Join(repo, filepath.FromSlash(f)), f+"\n", 0o644)
		}
		calls := claudeDirPassCalls(repo, gitArgvBudget)
		if c.want == nil {
			if calls != nil {
				t.Errorf("%s: argv %q, want no call (f6010b97)", c.name, calls)
			}
			continue
		}
		if len(calls) != 1 {
			t.Errorf("%s: %d calls, want 1 (f6010b97)", c.name, len(calls))
			continue
		}
		args := calls[0]
		if len(args) < len(prefix) || !slices.Equal(args[:len(prefix)], prefix) {
			t.Errorf("%s: argv %q, want the prefix %q (f6010b97)", c.name, args, prefix)
			continue
		}
		got := slices.Sorted(slices.Values(args[len(prefix):]))
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: pathspecs %q, want %q (f6010b97)", c.name, got, c.want)
		}
	}
}

// claudeChildren returns n distinct `.claude/` pathspecs whose child names are
// size bytes long: a lead byte, then zero-padded digits.
func claudeChildren(n, size int, lead string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = claudeDirName + "/" + lead + fmt.Sprintf("%0*d", size-len(lead), i)
	}
	return out
}

// TestClaudeDirPassBatches replays the batch layout of the f6010b97 `.claude/`
// pass (scratch/f6010b97/slice4-claude-many.md). The Windows rows use the
// Windows budget and the Linux rows use the Linux budget, so the test runs on
// every OS. Each pathspec costs its length plus 3 bytes, and the fixed argv
// costs 87. The bisection rows pin the split: a pathspec cost of 24 489 is one
// call on Windows and 24 490 is two. On Linux the edge is 130 985 and 130 986.
func TestClaudeDirPassBatches(t *testing.T) {
	const windows, unix = 24576, 131072
	if got := argvCost(claudeDirPassFixedArgs); got != 87 {
		t.Fatalf("fixed argv cost %d, want 87", got)
	}
	cost := func(paths []string) int { return argvCost(paths) }
	// bisectW is the Wb row: 788 children of 20 bytes, then one child of
	// l bytes that the directory read lists last.
	bisectW := func(l int) []string {
		return append(claudeChildren(788, 20, "c"), claudeDirName+"/"+strings.Repeat("z", l))
	}
	// bisectL is the Lb row: one child of l bytes, then 1844 children of 60
	// bytes. The long child sits in the first call, as on the Linux VM.
	bisectL := func(l int) []string {
		return append([]string{claudeDirName + "/" + strings.Repeat("z", l)}, claudeChildren(1844, 60, "c")...)
	}
	for _, c := range []struct {
		name     string
		budget   int
		paths    []string
		wantCost int
		want     []int
	}{
		{"Wc_n00003", windows, claudeChildren(3, 20, "c"), 93, []int{3}},
		{"Wc_n00790", windows, claudeChildren(790, 20, "c"), 24490, []int{789, 1}},
		{"Wc_n02400", windows, claudeChildren(2400, 20, "c"), 74400, []int{789, 789, 789, 33}},
		{"Ws_n01000", windows, claudeChildren(1000, 5, "s"), 16000, []int{1000}},
		{"Ws_n01600", windows, claudeChildren(1600, 5, "s"), 25600, []int{1530, 70}},
		{"Wb_l50", windows, bisectW(50), 24489, []int{789}},
		{"Wb_l51", windows, bisectW(51), 24490, []int{788, 1}},
		{"Lc_n01844", unix, claudeChildren(1844, 60, "c"), 130924, []int{1844}},
		{"Lc_n01845", unix, claudeChildren(1845, 60, "c"), 130995, []int{1844, 1}},
		{"Lc_n10000", unix, claudeChildren(10000, 60, "c"), 710000, []int{1844, 1844, 1844, 1844, 1844, 780}},
		{"Lb_l050", unix, bisectL(50), 130985, []int{1845}},
		{"Lb_l051", unix, bisectL(51), 130986, []int{1844, 1}},
	} {
		if got := cost(c.paths); got != c.wantCost {
			t.Fatalf("%s: fixture pathspec cost %d, want %d", c.name, got, c.wantCost)
		}
		calls := claudeDirPassBatches(c.paths, c.budget)
		var sizes []int
		var joined []string
		for _, call := range calls {
			if !slices.Equal(call[:len(claudeDirPassFixedArgs)], claudeDirPassFixedArgs) {
				t.Fatalf("%s: call starts %q, want %q", c.name, call[:len(claudeDirPassFixedArgs)], claudeDirPassFixedArgs)
			}
			sizes = append(sizes, len(call)-len(claudeDirPassFixedArgs))
			joined = append(joined, call[len(claudeDirPassFixedArgs):]...)
		}
		if !slices.Equal(sizes, c.want) {
			t.Errorf("%s: call sizes %v, want %v (f6010b97)", c.name, sizes, c.want)
		}
		if !slices.Equal(joined, c.paths) {
			t.Errorf("%s: the calls do not name each child once, in the order of the directory read", c.name)
		}
	}
	if calls := claudeDirPassBatches(nil, unix); calls != nil {
		t.Errorf("no children: %q, want no call", calls)
	}
}
