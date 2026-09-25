//go:build unix

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// gitStub configures stubGit. A zero value answers `git version` with the
// real git.
type gitStub struct {
	// version is the exact `git version` output, and versionRC its exit
	// status. If both are zero, the real git answers.
	version   string
	versionRC int
	// failWord makes each call that has it as a whole argument fail with
	// failRC. With failRun the real git runs first and its output is kept.
	// Without it the call prints nothing.
	failWord string
	failRun  bool
	failRC   int
}

// stubGit puts a `git` on PATH and returns its log directory. Each call appends
// its argv to calls.log. Each call whose whole argv is `version` also appends
// its working directory and argv to version.log, and writes its GIT_*
// environment to version.env. Every other call runs the real git, unless
// failWord matches one of its arguments.
//
// The stub tests each argument as a whole word. A match on "$*" also hits the
// words inside the `-c` values and the paths, such as a HOME under ~/.config.
func stubGit(t *testing.T, s gitStub) string {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found in PATH")
	}
	bin := t.TempDir()
	q := func(p string) string { return "'" + filepath.Join(bin, p) + "'" }
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + q("calls.log") + "\n" +
		"if [ \"$#\" -eq 1 ] && [ \"$1\" = version ]; then\n" +
		"  printf 'cwd=%s argv=%s\\n' \"$PWD\" \"$*\" >> " + q("version.log") + "\n" +
		"  env | grep '^GIT_' | LC_ALL=C sort > " + q("version.env") + "\n"
	if s.version != "" || s.versionRC != 0 {
		writeFile(t, filepath.Join(bin, "version.out"), s.version, 0o644)
		script += "  cat " + q("version.out") + "\n" +
			"  exit " + strconv.Itoa(s.versionRC) + "\n"
	}
	script += "fi\n"
	if s.failWord != "" {
		writeFile(t, filepath.Join(bin, "fail.word"), s.failWord, 0o644)
		run := "cat > /dev/null"
		if s.failRun {
			run = "'" + real + "' \"$@\""
		}
		script += "w=$(cat " + q("fail.word") + ")\n" +
			"for a in \"$@\"; do\n" +
			"  if [ \"$a\" = \"$w\" ]; then " + run + "; exit " + strconv.Itoa(s.failRC) + "; fi\n" +
			"done\n"
	}
	script += "exec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return bin
}

// stubVersionGit is stubGit with only a `git version` answer. It returns the
// version log file.
func stubVersionGit(t *testing.T, version string) string {
	t.Helper()
	return filepath.Join(stubGit(t, gitStub{version: version + "\n"}), "version.log")
}

func versionCalls(t *testing.T, log string) int {
	t.Helper()
	data, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "\n")
}

// TestIncludeVersionGate covers R2 end to end, with the G02 fixture: `build/`
// ignored and the manifest `*.txt`. The old scan copies `build/*`. The new scan
// copies nothing. `git version` runs once when the manifest is a regular file,
// and not at all without one (A00).
func TestIncludeVersionGate(t *testing.T) {
	requireGit(t)
	isolateGitConfig(t)
	g02 := includeCase{ignore: buildTreeIgnore, files: buildTreeFiles, manifest: "*.txt\n"}
	for _, c := range []struct {
		version string
		want    []string
	}{
		{"git version 2.31.9", buildTreeFiles},
		{"git version 2.32.0", nil},
		{"git version 2.31.99.windows.1", buildTreeFiles},
		{"git version 2.32.0.windows.1", nil},
		{"garbage", buildTreeFiles},
	} {
		t.Run(c.version, func(t *testing.T) {
			log := stubVersionGit(t, c.version)
			got := runIncludeCase(t, g02)
			want := slices.Clone(c.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("copied %v, want %v", got, want)
			}
			if n := versionCalls(t, log); n != 1 {
				t.Errorf("git version ran %d times with a manifest, want 1", n)
			}
			// f6010b97 runs it with no `-c` options and no `-C`, in the daemon's
			// own working directory.
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(log)
			if want := "cwd=" + cwd + " argv=version\n"; string(data) != want {
				t.Errorf("git version call = %q, want %q", data, want)
			}
		})
	}
	t.Run("no manifest runs no git version", func(t *testing.T) {
		log := stubVersionGit(t, "git version 2.32.0")
		runIncludeCase(t, includeCase{ignore: buildTreeIgnore, files: buildTreeFiles})
		if n := versionCalls(t, log); n != 0 {
			t.Errorf("git version ran %d times without a manifest, want 0", n)
		}
	})
}

// TestIncludeVersionExitAndSearch covers item 1 end to end. `git version` is
// found anywhere in the output. A number too large for an int still selects
// the new scan. A non-zero exit selects the old scan, even with valid output.
// The fixture is gate_tree. The new scan copies build/a.txt and r.ig, and the
// old scan also copies other/o.ig.
func TestIncludeVersionExitAndSearch(t *testing.T) {
	requireGit(t)
	isolateGitConfig(t)
	gate := includeCase{ignore: lines("build/", "other/", "*.ig"),
		files: []string{"build/a.txt", "other/o.ig", "r.ig"}, manifest: "build/\n*.ig\n"}
	newSet := []string{"build/a.txt", "r.ig"}
	oldSet := []string{"build/a.txt", "other/o.ig", "r.ig"}
	for _, c := range []struct {
		name, version string
		rc            int
		want          []string
	}{
		{"I01_ver_ctl_new", "git version 2.32.0\n", 0, newSet},
		{"I01_ver_ctl_old", "git version 2.31.0\n", 0, oldSet},
		{"I01_ver_lead1sp", " git version 2.32.0\n", 0, newSet},
		{"I01_ver_leadnl", "\ngit version 2.32.0\n", 0, newSet},
		{"I01_ver_junkfirst", "xx git version 2.32.0\n", 0, newSet},
		{"I01_ver_bigmajor", "git version 99999999999999999999.0\n", 0, newSet},
		{"I01_ver_bigminor", "git version 2.99999999999999999999\n", 0, newSet},
		{"I01_ver_rc1_new", "git version 2.50.0\n", 1, oldSet},
		{"I01_ver_rc128_new", "git version 2.50.0\n", 128, oldSet},
		{"I01_ver_rc128_empty", "", 128, oldSet},
	} {
		t.Run(c.name, func(t *testing.T) {
			stubGit(t, gitStub{version: c.version, versionRC: c.rc})
			got := runIncludeCase(t, gate)
			if !slices.Equal(got, c.want) {
				t.Errorf("copied %v, want %v (f6010b97)", got, c.want)
			}
		})
	}
}

// TestGitVersionEnvUnchanged covers item 17: `git version` gets the daemon's
// environment unchanged, with none of the hardened GIT_* variables.
func TestGitVersionEnvUnchanged(t *testing.T) {
	requireGit(t)
	isolateGitConfig(t)
	t.Setenv("GIT_TRACE_SETUP", "")
	bin := stubGit(t, gitStub{version: "git version 2.32.0\n"})
	if !gitSelectsIncludeScan() {
		t.Fatal("git version 2.32.0 selects the old scan")
	}
	var want []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") {
			want = append(want, kv)
		}
	}
	slices.Sort(want)
	data, err := os.ReadFile(filepath.Join(bin, "version.env"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"); !slices.Equal(got, want) {
		t.Errorf("git version GIT_* environment = %q, want the daemon's own %q", got, want)
	}
}

// TestIncludeGitErrorArms covers item 11 with the err_tree fixture. The stub
// fails one call, and the want lists are the f6010b97 file sets. The arms
// file_batch_128, dir_batch_128 and dir_batch_128_run are not measured file
// sets. Their want lists are derived from the measured item-11 rule.
func TestIncludeGitErrorArms(t *testing.T) {
	requireGit(t)
	isolateGitConfig(t)
	const v232, v231 = "git version 2.32.0\n", "git version 2.31.0\n"
	for _, c := range []struct {
		name    string
		version string
		fail    gitStub
		want    []string
	}{
		{"I11_ctl", v232, gitStub{}, errTreeNew},
		{"I11_listing_128", v232, gitStub{failWord: "--directory", failRC: 128}, nil},
		{"I11_listing_128_run", v232, gitStub{failWord: "--directory", failRun: true, failRC: 128}, nil},
		{"file_batch_128", v232, gitStub{failWord: "r.ig", failRC: 128}, []string{"build/a.txt", "sub/build/c.ig"}},
		{"dir_batch_128", v232, gitStub{failWord: "sub/build", failRC: 128}, []string{"r.ig"}},
		{"dir_batch_128_run", v232, gitStub{failWord: "sub/build", failRun: true, failRC: 128}, []string{"r.ig"}},
		{"I11_chk_128", v232, gitStub{failWord: "check-ignore", failRC: 128}, []string{"r.ig"}},
		{"I11_chk_128_run", v232, gitStub{failWord: "check-ignore", failRun: true, failRC: 128}, []string{"r.ig"}},
		{"I11_chk_1_run", v232, gitStub{failWord: "check-ignore", failRun: true, failRC: 1}, errTreeNew},
		{"I11_old_ctl", v231, gitStub{}, errTreeOld},
		{"I11_old_first_128", v231, gitStub{failWord: "--no-literal-pathspecs", failRC: 128}, nil},
		{"I11_old_chk_128", v231, gitStub{failWord: "check-ignore", failRC: 128}, nil},
		{"I11_old_chk_128_run", v231, gitStub{failWord: "check-ignore", failRun: true, failRC: 128}, nil},
		{"I11_old_stdlist_128", v231, gitStub{failWord: "--exclude-standard", failRC: 128}, errTreeOld},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := c.fail
			s.version = c.version
			stubGit(t, s)
			got := runIncludeCase(t, errTree)
			want := slices.Clone(c.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("copied %v, want %v (f6010b97)", got, want)
			}
		})
	}
}

// TestIncludeMagicLookingNames covers the check-ignore finding of item 16 with
// the magic_tree fixture. `git check-ignore` exits 128 on a name such as
// `:!z.ig`. The new scan sends no file candidate to it, so it copies all four
// files. The old scan sends every name to it, so it copies nothing.
func TestIncludeMagicLookingNames(t *testing.T) {
	requireGit(t)
	isolateGitConfig(t)
	magic := includeCase{ignore: lines("*.ig"), files: []string{"0.ig", ":!z.ig", "a.ig", "zz.ig"}, manifest: "*.ig\n"}
	for _, c := range []struct {
		name, version string
		want          []string
	}{
		{"I16g_magic_name_new", "git version 2.32.0\n", []string{"0.ig", ":!z.ig", "a.ig", "zz.ig"}},
		{"I16g_magic_name_old", "git version 2.31.0\n", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			stubGit(t, gitStub{version: c.version})
			if got := runIncludeCase(t, magic); !slices.Equal(got, c.want) {
				t.Errorf("copied %v, want %v (f6010b97)", got, c.want)
			}
		})
	}
}

// TestIncludePrefixOpeningCopies covers the prefix rule end to end, with the
// esctree dirs that decide it. The names hold a `\`, so the test runs on Linux
// and macOS only. `a\ b` opens every root directory that starts with `a`. So
// `*.txt` copies a file from each, and git's own match of `a\ b` adds nothing.
func TestIncludePrefixOpeningCopies(t *testing.T) {
	esc := func(name, manifest string, want ...string) includeCase {
		return includeCase{name: name, ignore: lines("/a*/", "/x/"),
			files:    []string{"a b/f.txt", "a/f.txt", `a\ b/f.txt`, "ab/f.txt", "x/f.txt"},
			manifest: manifest, want: want}
	}
	checkIncludeCases(t, []includeCase{
		esc("a_bs_sp_b_txt", "a\\ b\n*.txt\n", "a b/f.txt", "a/f.txt", `a\ b/f.txt`, "ab/f.txt"),
		esc("a_bs_sp_b_alone", "a\\ b/\n", "a b/f.txt"),
		esc("a_sp_b_ctl_txt", "a b\n*.txt\n", "a b/f.txt"),
	})
}

// TestIncludeLinuxMeasuredCopies covers two opening rules end to end, with the
// f6010b97 file sets measured on a Linux VM. `sub\/x/` opens `sub/x`, because
// its first segment ends in a lone `\` and so matches any name. Git reads the
// line as `sub/x/`. `build//a.txt` opens only `build`, and `*.txt` then copies
// `build/a.txt`. It does not open `sub/build` or `a/x/build`.
func TestIncludeLinuxMeasuredCopies(t *testing.T) {
	checkIncludeCases(t, []includeCase{
		{name: "L4_sub_bs_sl_x_alone", ignore: lines("/ab/", "/sub/x/", "/subq/"), tracked: []string{"sub/t.txt"},
			files: []string{"ab/f.txt", "sub/x/f.txt", "subq/f.txt"}, manifest: "sub\\/x/\n",
			want: []string{"sub/x/f.txt"}},
		{name: "L3_ds_file_txt", ignore: bld3Ignore, files: bld3Files, manifest: "build//a.txt\n*.txt\n",
			want: []string{".bx/d.txt", "build/a.txt"}},
	})
}

// TestIncludeManifestShapeCallShape covers R1 with the D10 to D12 fixtures. An
// empty regular file still runs `git version` and the scan, and it copies
// nothing. With the new scan, f6010b97 runs the listing and one file batch.
// With the old scan, it runs one `ls-files`. A symlink or a directory runs no
// `git version` and no scan.
func TestIncludeManifestShapeCallShape(t *testing.T) {
	requireGit(t)
	isolateGitConfig(t)
	emptyFile := func(t *testing.T, repo string) {
		writeFile(t, filepath.Join(repo, worktreeIncludeFile), "", 0o644)
	}
	symlink := func(t *testing.T, repo string) {
		writeFile(t, filepath.Join(repo, "real.inc"), "a.i\n", 0o644)
		if err := os.Symlink("real.inc", filepath.Join(repo, worktreeIncludeFile)); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name, version string
		setup         func(*testing.T, string)
		files         []string
		// want lists the manifest-pass calls, by their distinct argument.
		want []string
	}{
		{"D12_empty_new", "git version 2.32.0\n", emptyFile, []string{"a.i"},
			[]string{"version", "--directory", "--literal-pathspecs"}},
		{"D12_empty_old", "git version 2.31.0\n", emptyFile, []string{"a.i"},
			[]string{"version", "--no-literal-pathspecs"}},
		{"D10_symlink", "git version 2.32.0\n", symlink, []string{"a.i"}, nil},
		{"D11_dir", "git version 2.32.0\n", nil, []string{"a.i", ".worktreeinclude/x"}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			bin := stubGit(t, gitStub{version: c.version})
			got := runIncludeCase(t, includeCase{ignore: lines("*.i"), files: c.files, setup: c.setup})
			if len(got) != 0 {
				t.Errorf("copied %v, want nothing (f6010b97)", got)
			}
			data, err := os.ReadFile(filepath.Join(bin, "calls.log"))
			if err != nil {
				t.Fatal(err)
			}
			var calls []string
			for _, line := range strings.Split(string(data), "\n") {
				for _, arg := range strings.Fields(line) {
					if tmp, ok := strings.CutPrefix(arg, "--exclude-from="); ok {
						checkIncludeTempName(t, filepath.Base(tmp))
					}
				}
				switch {
				case line == "version":
					calls = append(calls, "version")
				case strings.Contains(line, "check-ignore"):
					calls = append(calls, "check-ignore")
				case !strings.Contains(line, "ls-files"):
				case strings.Contains(line, "--directory"):
					calls = append(calls, "--directory")
				case strings.Contains(line, "--no-literal-pathspecs"):
					calls = append(calls, "--no-literal-pathspecs")
				case strings.Contains(line, "--literal-pathspecs"):
					calls = append(calls, "--literal-pathspecs")
				}
			}
			if !slices.Equal(calls, c.want) {
				t.Errorf("manifest-pass git calls = %q, want %q (f6010b97)", calls, c.want)
			}
		})
	}
}

// TestBatchIncludePathspecs covers R17. The counts are the measured paths per
// batch for each path length. Each path costs its length plus 3 bytes.
func TestBatchIncludePathspecs(t *testing.T) {
	for length, perBatch := range map[int]int{
		11: 9350, 12: 8726, 13: 8181, 16: 6889, 20: 5691, 40: 3044,
		45: 2727, 50: 2469, 100: 1270, 200: 644, 250: 517,
	} {
		paths := make([]string, 2*perBatch+1)
		for i := range paths {
			paths[i] = fmt.Sprintf("%0*d", length, i)
		}
		batches := batchIncludePathspecs(paths, gitArgvBudget-argvCost(includeBatchArgs(strings.Repeat("t", 101))))
		if len(batches) != 3 || len(batches[0]) != perBatch || len(batches[1]) != perBatch || len(batches[2]) != 1 {
			sizes := make([]int, len(batches))
			for i, b := range batches {
				sizes[i] = len(b)
			}
			t.Errorf("length %d: batch sizes %v, want [%d %d 1]", length, sizes, perBatch, perBatch)
		}
	}
	// A path larger than the limit still gets a batch of its own.
	huge := strings.Repeat("h", gitArgvBudget)
	if got := batchIncludePathspecs([]string{"a", huge, "b"}, gitArgvBudget); len(got) != 3 {
		t.Errorf("an oversize path shares a batch: %d batches, want 3", len(got))
	}
}

// TestPlanIncludeScanBatchLayout covers item 10 with the batch fixtures. The
// batch sizes depend on the length of the `--exclude-from` argument. It was 56
// or 57 bytes on the Linux VM, and 100 or 101 bytes on the macOS VM. The want sizes are
// the f6010b97 batch sizes with the same argument length.
func TestPlanIncludeScanBatchLayout(t *testing.T) {
	names := func(n int, format string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf(format, i)
		}
		return out
	}
	listing := func(files, dirs []string) string {
		var ents []string
		for _, d := range dirs {
			ents = append(ents, d+"/")
		}
		ents = append(ents, files...)
		slices.Sort(ents)
		return ".claude/\x00.claude/worktrees/\x00" + strings.Join(ents, "\x00") + "\x00"
	}
	sizes := func(bs [][]string) []int {
		out := []int{}
		for _, b := range bs {
			out = append(out, len(b))
		}
		return out
	}
	dirs := names(9000, "d%011d")
	pads := names(4000, "P%06d"+strings.Repeat("x", 30)+".pad")
	for _, c := range []struct {
		name         string
		argLen       int
		manifest     string
		files, dirs  []string
		wantF, wantD []int
	}{
		{"I10a_linux", 56, "d*/\n", nil, dirs, []int{}, []int{8729, 271}},
		{"I10a_macos", 101, "d*/\n", nil, dirs, []int{}, []int{8726, 274}},
		{"I10b_linux", 57, "d*/\n", names(100, "f%011d.pad"), dirs, []int{100}, []int{8729, 271}},
		{"I10b_macos", 100, "d*/\n", names(100, "f%011d.pad"), dirs, []int{100}, []int{8726, 274}},
		{"I10c_linux", 56, "d*/\n", names(8000, "f%07d.pd"), dirs, []int{8000}, []int{8729, 271}},
		{"I10c_macos", 100, "d*/\n", names(8000, "f%07d.pd"), dirs, []int{8000}, []int{8726, 274}},
		{"I11m_linux", 57, "build/\nP0000*\nP0039*\n", pads, []string{"build"}, []int{2976, 1024}, []int{1}},
		{"I11m_macos", 101, "build/\nP0000*\nP0039*\n", pads, []string{"build"}, []int{2975, 1025}, []int{1}},
	} {
		arg := "--exclude-from=" + strings.Repeat("t", c.argLen-len("--exclude-from="))
		p := planIncludeScan([]byte(c.manifest), listing(c.files, c.dirs), arg)
		if gotF, gotD := sizes(p.fileBatches), sizes(p.dirBatches); !slices.Equal(gotF, c.wantF) || !slices.Equal(gotD, c.wantD) {
			t.Errorf("%s: file batches %v, dir batches %v, want %v and %v (f6010b97)", c.name, gotF, gotD, c.wantF, c.wantD)
		}
		for _, b := range p.dirBatches {
			for _, d := range b {
				if strings.HasSuffix(d, "/") {
					t.Fatalf("%s: directory pathspec %q ends in /", c.name, d)
				}
			}
		}
	}
}

// TestClaudeDirPassCallShape covers the `.claude/` pass end to end with the
// logged git argv. f6010b97 names each child of `.claude/` except `worktrees`
// (I14b, C01), and it runs no call when `.claude/` holds only `worktrees`
// (I14a). I15d shows that `Worktrees` is left out too. It runs on Linux only,
// because it needs a case-sensitive file system.
func TestClaudeDirPassCallShape(t *testing.T) {
	requireGit(t)
	isolateGitConfig(t)
	const pass = "--literal-pathspecs ls-files --others --ignored --exclude-standard -z --"
	for _, c := range []struct {
		name  string
		c     includeCase
		linux bool
		want  []string
	}{
		{"I14a_std", includeCase{files: []string{".claude/worktrees/stale/x.dd", "top.dd"}, ignore: lines("top.dd"),
			manifest: "*.dd\n"}, false, nil},
		{"I14b_claude_whole_ignored", includeCase{ignore: lines(".claude/"),
			files: []string{".claude/worktrees/stale/x.dd", ".claude/keep.dd", "top.dd"}, manifest: "*.dd\n"},
			false, []string{".claude/keep.dd"}},
		{"C01_claude_no_manifest", includeCase{ignore: lines(".claude/settings.local.json", ".claude/checkpoints/"),
			files: []string{".claude/settings.local.json", ".claude/checkpoints/x.json", ".claude/untr.json"}},
			false, []string{".claude/checkpoints", ".claude/settings.local.json", ".claude/untr.json"}},
		{"I15d_case_new", includeCase{ignore: lines(".claude/"),
			files: []string{".claude/Worktrees/x.dd", ".claude/CHECKPOINTS/c.dd", ".claude/k.dd"}, manifest: "*.dd\n"},
			true, []string{".claude/CHECKPOINTS", ".claude/k.dd"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.linux && runtime.GOOS != "linux" {
				t.Skip("needs a case-sensitive file system")
			}
			bin := stubGit(t, gitStub{version: "git version 2.32.0\n"})
			runIncludeCase(t, c.c)
			data, err := os.ReadFile(filepath.Join(bin, "calls.log"))
			if err != nil {
				t.Fatal(err)
			}
			var calls [][]string
			for _, line := range strings.Split(string(data), "\n") {
				if strings.Contains(line, "--exclude-standard") && !strings.Contains(line, "--directory") {
					_, rest, ok := strings.Cut(line, " "+pass+" ")
					if !ok {
						t.Fatalf("the .claude pass ran %q, want %q and pathspecs (f6010b97)", line, pass)
					}
					calls = append(calls, slices.Sorted(slices.Values(strings.Fields(rest))))
				}
			}
			switch {
			case c.want == nil && len(calls) != 0:
				t.Errorf("the .claude pass ran %q, want no call (f6010b97)", calls)
			case c.want != nil && (len(calls) != 1 || !slices.Equal(calls[0], c.want)):
				t.Errorf("the .claude pass named %q, want one call naming %q (f6010b97)", calls, c.want)
			}
		})
	}
}

// TestIncludeUnixConstants pins the Linux and macOS argv budget. It is fitted
// to the f6010b97 batch counts.
func TestIncludeUnixConstants(t *testing.T) {
	if gitArgvBudget != 131072 {
		t.Errorf("gitArgvBudget = %d, want 131072 (fitted to the Linux and macOS batch counts)", gitArgvBudget)
	}
}

// TestClaudeDirPassBatchCalls runs the `.claude/` pass with 1845 children of
// 60 bytes, the Lc_n01845 row. f6010b97 makes two `ls-files` calls and runs
// `config -z --list --name-only` before each one. Every child is copied.
func TestClaudeDirPassBatchCalls(t *testing.T) {
	requireGit(t)
	isolateGitConfig(t)
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	writeFile(t, filepath.Join(repo, ".gitignore"), ".claude/\n", 0o644)
	children := claudeChildren(1845, 60, "c")
	for _, p := range children {
		writeFile(t, filepath.Join(repo, filepath.FromSlash(p)), "x\n", 0o644)
	}
	wt := t.TempDir()
	calls := logGitArgv(t)
	copyClaudeDir(repo, wt)
	var shape []string
	for _, c := range calls() {
		switch {
		case slices.Equal(c, []string{"config", "--includes", "--path", "core.excludesFile"}):
			// The resolver of the user's excludes file, not part of the pass.
		case slices.Equal(c, []string{"config", "-z", "--list", "--name-only"}):
			shape = append(shape, "config")
		case len(c) > len(claudeDirPassFixedArgs) && slices.Equal(c[:len(claudeDirPassFixedArgs)], claudeDirPassFixedArgs):
			shape = append(shape, fmt.Sprintf("ls-files(%d)", len(c)-len(claudeDirPassFixedArgs)))
		default:
			shape = append(shape, strings.Join(c, " "))
		}
	}
	want := []string{"config", "ls-files(1844)", "config", "ls-files(1)"}
	if !slices.Equal(shape, want) {
		t.Errorf("git calls %q, want %q (f6010b97)", shape, want)
	}
	for _, p := range children {
		if _, err := os.Stat(filepath.Join(wt, filepath.FromSlash(p))); err != nil {
			t.Fatalf("%s not copied: %v", p, err)
		}
	}
}

// TestClaudeDirPassFailedBatch covers a failed `.claude/` batch. Claustrum
// skips that batch and copies the other batches, as the directory batches of
// the scan do.
func TestClaudeDirPassFailedBatch(t *testing.T) {
	requireGit(t)
	isolateGitConfig(t)
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	writeFile(t, filepath.Join(repo, ".gitignore"), ".claude/\n", 0o644)
	for _, p := range []string{".claude/a", ".claude/bad", ".claude/b"} {
		writeFile(t, filepath.Join(repo, filepath.FromSlash(p)), "x\n", 0o644)
	}
	stubGit(t, gitStub{failWord: ".claude/bad", failRun: true, failRC: 128})
	wt := t.TempDir()
	fixed := claudeDirPassFixedArgs
	copyClaudeDirCalls(repo, wt, [][]string{
		append(slices.Clone(fixed), ".claude/a", ".claude/bad"),
		append(slices.Clone(fixed), ".claude/b"),
	})
	for p, want := range map[string]bool{".claude/a": false, ".claude/bad": false, ".claude/b": true} {
		_, err := os.Stat(filepath.Join(wt, filepath.FromSlash(p)))
		if got := err == nil; got != want {
			t.Errorf("%s copied = %v, want %v", p, got, want)
		}
	}
}
