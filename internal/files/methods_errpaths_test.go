package files

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// files.stat on a path that does not exist reports the zero facts object with
// exists:false — a result, never an error (matching the reference).
func TestFilesStatNonexistent(t *testing.T) {
	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "files.stat",
		map[string]any{"path": filepath.Join(t.TempDir(), "absent")}))
	if !strings.Contains(got, `"exists":false`) {
		t.Errorf("files.stat nonexistent = %s, want exists:false result", got)
	}
}

// files.list surfaces an unreadable directory as -32603 (unlike files.stat's
// soft empty result — probe-verified asymmetry).
func TestFilesListNonexistentDir(t *testing.T) {
	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "files.list",
		map[string]any{"path": filepath.Join(t.TempDir(), "absent")}))
	if !strings.Contains(got, `"code":-32603`) {
		t.Errorf("files.list nonexistent = %s, want -32603", got)
	}
}

// tgzEntry describes one member of a crafted archive for the extractTarGz
// error-path tests; makeTarGz can't express directories or member ordering.
type tgzEntry struct {
	name string
	dir  bool
	body string
}

// writeTgz materializes entries into a .tar.gz at path, optionally truncating
// the inner tar stream to truncAt bytes (0 = no truncation) to simulate an
// archive cut off mid-member.
func writeTgz(t *testing.T, path string, entries []tgzEntry, truncAt int) {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o755, Typeflag: tar.TypeDir}
		if !e.dir {
			hdr = &tar.Header{Name: e.name, Mode: 0o644, Typeflag: tar.TypeReg, Size: int64(len(e.body))}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if !e.dir {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	if truncAt > 0 {
		raw = raw[:truncAt]
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	if _, err := gz.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// extractTarGz's per-member failure arms: each malicious/degenerate archive
// must surface an error instead of a partial silent success.
func TestExtractTarGzMemberErrors(t *testing.T) {
	cases := []struct {
		name    string
		entries []tgzEntry
		count   int
	}{
		// a directory member whose path runs through an already-extracted
		// regular file -> TypeDir MkdirAll fails. This arm answers fileCount 0.
		{"dir through file", []tgzEntry{{name: "f", body: "x"}, {name: "f/sub", dir: true}}, 0},
		// a file member nested under an already-extracted regular file ->
		// parent MkdirAll fails
		{"file under file", []tgzEntry{{name: "f", body: "x"}, {name: "f/sub/x", body: "y"}}, 0},
		// a file member colliding with an already-extracted directory ->
		// OpenFile fails
		{"file over dir", []tgzEntry{{name: "d", dir: true}, {name: "d", body: "x"}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "a.tar.gz")
			writeTgz(t, archive, tc.entries, 0)
			n, err := extractTarGz(archive, filepath.Join(t.TempDir(), "out"))
			if err == nil {
				t.Error("extractTarGz succeeded, want error")
			}
			if n != tc.count {
				t.Errorf("fileCount = %d, want %d", n, tc.count)
			}
		})
	}
}

// An archive truncated mid-member data errors out of the io.Copy arm
// (unexpected EOF from the tar reader), not a silent short extraction.
func TestExtractTarGzTruncatedMember(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "trunc.tar.gz")
	// One 600-byte file: header block [0,512), data [512,1536). Cutting at 900
	// leaves the member's advertised size unsatisfiable.
	writeTgz(t, archive, []tgzEntry{{name: "f", body: strings.Repeat("x", 600)}}, 900)
	_, err := extractTarGz(archive, filepath.Join(t.TempDir(), "out"))
	if err == nil || !strings.HasPrefix(err.Error(), "write f: ") {
		t.Errorf("extractTarGz on truncated archive = %v, want a \"write f: \" error", err)
	}
}
