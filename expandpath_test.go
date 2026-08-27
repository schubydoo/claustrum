package main

import (
	"path/filepath"
	"runtime"
	"testing"
)

// TestExpandPath pins the rule as a unit, alongside the socket-level golden.
// Every expectation here was probe-measured against the reference daemon at
// 5db5e4a on 2026-08-02, reading the string the reference echoes back from
// git.worktree_create with the absolute form sent alongside as the control.
//
// CORRECTION, 2026-08-02: this comment previously dated the measurement to
// 2026-07-30 and claimed it covered "~/" and "~//f". It did not. That probe
// asserted with files.validate, whose reply is {valid,isDir} and echoes no path,
// so it could not have distinguished <home>/f.txt from <home>//f.txt. The two
// rows it recorded were inferred, and both were wrong — see expandpath.go.
func TestExpandPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Not because Windows is unmeasured — it is, see expandpath.go. The
		// expectations below are written in Unix spelling ("~/f.txt" -> home +
		// "/f.txt"), and the reference rewrites "/" to "\" there, so this table
		// pins the Unix form only. TestExpandPathBareTildeIsNotCleaned covers
		// the one row whose behaviour is spelling-independent.
		t.Skip("this table is written in Unix path spelling; Windows is pinned in expandpath.go")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	for _, tc := range []struct {
		name, in, want string
	}{
		{"bare tilde", "~", home},
		{"file under home", "~/f.txt", home + "/f.txt"},
		// A tilde path is CLEANED. The comment here used to claim the opposite —
		// that cleaning "would diverge from the reference" — which was an
		// assumption, not a measurement. It is wrong: with ~/link -> b/c the
		// reference reads ~/x.txt for "~/link/../x.txt" while an uncleaned path
		// walks the symlink and reads ~/b/x.txt instead. See
		// TestExpandPathCleansTildeLexically.
		//
		// These two rows ARE measured reference output, and they ARE externally
		// observable — an earlier revision of this comment claimed neither. The
		// expanded string is echoed back by git.worktree_create's result.path and
		// appears in the error text of files.stat / read / list / validate,
		// files.extract_tar and process.spawn, so the spelling is wire-visible on
		// eight frames. "Opens the same file" was the wrong question.
		{"trailing slash", "~/", home},
		{"doubled slash", "~//f.txt", home + "/f.txt"},
		{"dot segment", "~/a/./b", home + "/a/b"},
		{"parent segment", "~/a/x/../b", home + "/a/b"},
		// Everything below must be returned untouched.
		{"tilde user", "~root/f.txt", "~root/f.txt"},
		{"tilde unknown user", "~nosuchuser/f.txt", "~nosuchuser/f.txt"},
		{"mid-path tilde", "/tmp/~/f.txt", "/tmp/~/f.txt"},
		{"tilde inside a name", "/tmp/a~b/f.txt", "/tmp/a~b/f.txt"},
		{"env var", "$HOME/f.txt", "$HOME/f.txt"},
		{"absolute", "/etc/hosts", "/etc/hosts"},
		{"relative", "sub/f.txt", "sub/f.txt"},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := expandPath(tc.in); got != tc.want {
				t.Errorf("expandPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestExpandPathBareTildeIsNotCleaned pins the one place the reference does NOT
// clean. Bare "~" echoes the home directory verbatim; "~/" under the same home is
// cleaned. Measured at 5db5e4a on 2026-08-02 by setting HOME to each unclean value
// below and reading git.worktree_create's echoed result.path, with "~" and "~/"
// sent in the same daemon run so the pair is its own control.
//
// This needs a deliberately unclean HOME to be visible at all: with the tidy
// t.TempDir() that TestExpandPath uses, cleaned and uncleaned agree, which is why
// that test's "bare tilde" row passes either way and cannot catch a regression
// here.
func TestExpandPathBareTildeIsNotCleaned(t *testing.T) {
	// Runs on Windows too: bare "~" is measured verbatim on BOTH platforms (a home
	// of "C:\h\" echoes back with its trailing "\"), and the assertions below are
	// built from the OS separator rather than a hardcoded "/", so they are not the
	// Unix-spelling expectations that keep the other tilde tests off Windows.
	// os.UserHomeDir reads USERPROFILE there, not HOME — setting the wrong one
	// would leave the real home in place and make every row vacuous.
	homeVar := "HOME"
	if runtime.GOOS == "windows" {
		homeVar = "USERPROFILE"
	}
	sep := string(filepath.Separator)
	base := t.TempDir()
	// Subtests are named for the SHAPE, not the path. The path embeds t.TempDir(),
	// so a path-named subtest is unique per run: CI uploads its JUnit XML to
	// Codecov Test Analytics from all three legs, which keys on test name, so
	// per-run names can never accumulate flake history and the catalogue grows
	// without bound. It would also put the runner's temp path in an uploaded
	// artifact. Both t.Errorf calls below already carry the actual home.
	for _, tc := range []struct{ name, home string }{
		{"trailing-sep", base + sep},
		{"dot-segment", base + sep + "." + sep},
		{"doubled-sep", base + sep + sep},
	} {
		home := tc.home
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(homeVar, home)
			if got := expandPath("~"); got != home {
				t.Errorf("expandPath(%q) with HOME=%q = %q, want the home verbatim", "~", home, got)
			}
			// The control: the same home, one character more in the request, and
			// the reference cleans it. If this ever equals the row above, the
			// probe that produced both was blind and the pair proves nothing.
			if got := expandPath("~/"); got != base {
				t.Errorf("expandPath(%q) with HOME=%q = %q, want %q", "~/", home, got, base)
			}
		})
	}
}

// TestExpandPathWithoutHome covers the fallback: an unresolvable home leaves the
// path untouched rather than failing the request, so a daemon started without
// HOME behaves exactly as it did before expansion existed.
func TestExpandPathWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", "")
	}
	for _, in := range []string{"~", "~/", "~/f.txt"} {
		if got := expandPath(in); got != in {
			t.Errorf("expandPath(%q) with no home = %q, want it unchanged", in, got)
		}
	}
}

// TestExpandPathsCoversEveryPathField guards the per-struct wiring: a params
// struct can implement expandPaths and still miss one of its own path fields,
// which the compile-time interface cannot catch.
func TestExpandPathsCoversEveryPathField(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Not because Windows is unmeasured — it is, see expandpath.go. The
		// expectations below are written in Unix spelling ("~/f.txt" -> home +
		// "/f.txt"), and the reference rewrites "/" to "\" there, so this table
		// pins the Unix form only. TestExpandPathBareTildeIsNotCleaned covers
		// the one row whose behaviour is spelling-independent.
		t.Skip("this table is written in Unix path spelling; Windows is pinned in expandpath.go")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	under := func(rest string) string { return filepath.Join(home, rest) }

	pp := &pathParams{Path: "~/p"}
	pp.expandPaths()
	if pp.Path != under("p") {
		t.Errorf("pathParams.Path = %q", pp.Path)
	}

	et := &extractTarParams{ArchivePath: "~/a.tgz", DestDir: "~/d"}
	et.expandPaths()
	if et.ArchivePath != under("a.tgz") || et.DestDir != under("d") {
		t.Errorf("extractTarParams = %+v", et)
	}

	gp := &gitParams{Path: "~/g", BaseRepo: "~/b", WorktreePath: "~/w", BranchName: "~keep", SourceBranch: "~keep2"}
	gp.expandPaths()
	if gp.Path != under("g") || gp.BaseRepo != under("b") || gp.WorktreePath != under("w") {
		t.Errorf("gitParams paths = %+v", gp)
	}
	// Branch names are refs, not paths: a branch legitimately called "~keep"
	// must survive untouched.
	if gp.BranchName != "~keep" || gp.SourceBranch != "~keep2" {
		t.Errorf("gitParams expanded a ref: BranchName=%q SourceBranch=%q", gp.BranchName, gp.SourceBranch)
	}

	sp := &spawnParams{Cwd: "~/c", Command: "~/not-a-path", ID: "~id"}
	sp.expandPaths()
	if sp.Cwd != under("c") {
		t.Errorf("spawnParams.Cwd = %q", sp.Cwd)
	}
	// Command and ID are not path params in the reference; leave them alone.
	if sp.Command != "~/not-a-path" || sp.ID != "~id" {
		t.Errorf("spawnParams touched a non-path field: %+v", sp)
	}
}

// The process-control params carry no filesystem paths; their expandPaths is an
// explicit no-op. Pin that nothing — not even a "~"-shaped id — is rewritten.
func TestExpandPathsProcessControlParamsAreNoOps(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	st := &stdinParams{ID: "~id", Data: "~/data"}
	st.expandPaths()
	if st.ID != "~id" || st.Data != "~/data" {
		t.Errorf("stdinParams mutated: %+v", st)
	}
	k := &killParams{ID: "~id", Signal: "~/TERM"}
	k.expandPaths()
	if k.ID != "~id" || k.Signal != "~/TERM" {
		t.Errorf("killParams mutated: %+v", k)
	}
	kw := &killAndWaitParams{ID: "~id", Signal: "~/TERM"}
	kw.expandPaths()
	if kw.ID != "~id" || kw.Signal != "~/TERM" {
		t.Errorf("killAndWaitParams mutated: %+v", kw)
	}
	r := &reattachParams{ID: "~id", FromSeq: 7}
	r.expandPaths()
	if r.ID != "~id" || r.FromSeq != 7 {
		t.Errorf("reattachParams mutated: %+v", r)
	}
}
