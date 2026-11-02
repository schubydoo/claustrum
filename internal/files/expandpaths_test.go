package files

import (
	"path/filepath"
	"runtime"
	"testing"
)

// TestExpandPathsCoversEveryPathField guards the per-struct wiring: a params
// struct can implement ExpandPaths and still miss one of its own path fields,
// which the compile-time interface cannot catch.
func TestExpandPathsCoversEveryPathField(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Not because Windows is unmeasured — it is, see internal/pathcanon/expandpath.go. The
		// expectations below are written in Unix spelling ("~/f.txt" -> home +
		// "/f.txt"), and the reference rewrites "/" to "\" there, so this table
		// pins the Unix form only.
		t.Skip("this table is written in Unix path spelling; Windows is pinned in internal/pathcanon/expandpath.go")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	under := func(rest string) string { return filepath.Join(home, rest) }

	pp := &pathParams{Path: "~/p"}
	pp.ExpandPaths()
	if pp.Path != under("p") {
		t.Errorf("pathParams.Path = %q", pp.Path)
	}

	lp := &listParams{Path: "~/l"}
	lp.ExpandPaths()
	if lp.Path != under("l") {
		t.Errorf("listParams.Path = %q", lp.Path)
	}

	rp := &fileReadParams{Path: "~/r"}
	rp.ExpandPaths()
	if rp.Path != under("r") {
		t.Errorf("fileReadParams.Path = %q", rp.Path)
	}

	et := &extractTarParams{ArchivePath: "~/a.tgz", DestDir: "~/d"}
	et.ExpandPaths()
	if et.ArchivePath != under("a.tgz") || et.DestDir != under("d") {
		t.Errorf("extractTarParams = %+v", et)
	}
}
