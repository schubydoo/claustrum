//go:build unix

package files

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// files.read of a file that cannot be opened surfaces the open error as -32603
// (distinct from the soft exists:false for a missing path).
func TestFilesReadUnreadableFile(t *testing.T) {
	skipIfRoot(t)
	p := filepath.Join(t.TempDir(), "sealed")
	if err := os.WriteFile(p, []byte("x"), 0o000); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "files.read", map[string]any{"path": p}))
	if !strings.Contains(got, `"code":-32603`) {
		t.Errorf("files.read unreadable = %s, want -32603", got)
	}
}

// The open of files.read comes before the directory test and before the size
// test. A directory of mode 0000 answers the open error, not "path is a
// directory". A file of mode 0000 over maxBytes answers the open error, not
// "file exceeds maxBytes". 5fd08069 answers the same (measured on Linux and
// macOS).
func TestFilesReadOpenErrorComesBeforeKindAndSize(t *testing.T) {
	skipIfRoot(t)
	root := t.TempDir()
	dir := filepath.Join(root, "sealed-dir")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "sealed-file")
	if err := os.WriteFile(file, []byte("abcd"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dir, file} {
		if err := os.Chmod(p, 0); err != nil {
			t.Fatal(err)
		}
	}
	// The temp folder cannot be removed with a directory of mode 0000 in it.
	t.Cleanup(func() {
		_ = os.Chmod(dir, 0o700)
		_ = os.Chmod(file, 0o600)
	})

	s := newTestServer(t)
	for _, tc := range []struct {
		name   string
		params map[string]any
		path   string
	}{
		{"directory of mode 0000", map[string]any{"path": dir}, dir},
		{"file of mode 0000 over maxBytes", map[string]any{"path": file, "maxBytes": 1}, file},
	} {
		got := dispatchRaw(t, s, rpcLine(t, "files.read", tc.params))
		want := `"error":{"code":-32603,"message":"open ` + tc.path + `: permission denied"}`
		if !strings.Contains(got, want) {
			t.Errorf("%s: files.read = %s, want %s", tc.name, got, want)
		}
	}
}

// extractTarGz's destDir preparation arms: a read-only parent denies both the
// RemoveAll of a pre-existing destDir and the MkdirAll of a fresh one.
func TestExtractTarGzDestDirDenied(t *testing.T) {
	skipIfRoot(t)
	// extractTarGz consumes its archive on every outcome, so each attempt
	// below gets a fresh one.
	freshArchive := func() string {
		archive := filepath.Join(t.TempDir(), "a.tar.gz")
		writeTgz(t, archive, []tgzEntry{{name: "f", body: "x"}}, 0)
		return archive
	}

	parent := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	occupied := filepath.Join(parent, "dest")
	if err := os.WriteFile(occupied, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	// dest exists but its parent denies the unlink -> RemoveAll arm
	if _, err := extractTarGz(freshArchive(), occupied); err == nil ||
		!strings.Contains(err.Error(), "clean destDir") {
		t.Errorf("extractTarGz into undeletable dest = %v, want clean destDir error", err)
	}
	// dest absent and its parent denies the create -> MkdirAll arm
	if _, err := extractTarGz(freshArchive(), filepath.Join(parent, "fresh")); err == nil ||
		!strings.Contains(err.Error(), "mkdir destDir") {
		t.Errorf("extractTarGz into uncreatable dest = %v, want mkdir destDir error", err)
	}
}
