package main

import (
	"path/filepath"
	"runtime"
	"testing"
)

// TestExpandPath pins the rule as a unit, alongside the socket-level golden.
// Every expectation here was probe-measured against the reference daemon at
// 5db5e4a on 2026-07-30 — including "~/" and "~//f", which the sweep had not
// recorded.
func TestExpandPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the reference was measured on Unix; the ~\\ form is unmeasured")
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
		// These two rows follow from that same Clean. Their difference is NOT
		// externally observable on its own — "~/" and "~//f.txt" open exactly the
		// same file either way, because the OS collapses the slashes — so they
		// are asserted for internal consistency, not as measured reference output.
		{"trailing slash", "~/", home},
		{"doubled slash", "~//f.txt", home + "/f.txt"},
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
		t.Skip("the reference was measured on Unix; the ~\\ form is unmeasured")
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
