//go:build linux || darwin

package childrec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRecordCallsWithNoUsableRunDir covers the record calls of a manager whose run dir
// cannot be opened, and the two gates of the removal. A write that cannot open the
// run dir logs the cannot-record line. A removal there, a removal with no run dir,
// and a removal for a pid below 2 do nothing and log nothing.
func TestRecordCallsWithNoUsableRunDir(t *testing.T) {
	logs := captureLogBuf(t)
	m := &Records{}
	m.RunDir = filepath.Join(t.TempDir(), "gone")
	m.writeRecord(childRecord{Pid: 4242, Start: "1", Argv0: "a"})
	if want := "[process.Registry] cannot record child 4242: "; !strings.Contains(logs.String(), want) {
		t.Errorf("log = %q\nwant it to hold %q", logs.String(), want)
	}
	logs.Reset()
	m.RemoveChildRecord(4242)
	(&Records{}).RemoveChildRecord(4242)

	held := &Records{}
	held.RunDir = t.TempDir()
	if err := writeChildRecord(held.RunDir, childRecord{Pid: 1, Start: "1", Argv0: "a"}); err != nil {
		t.Fatal(err)
	}
	held.RemoveChildRecord(1)
	if _, err := os.Stat(filepath.Join(held.RunDir, "children", "1.json")); err != nil {
		t.Errorf("the removal acted for a pid below 2: %v", err)
	}
	// A record that is already gone is not a failed removal.
	held.RemoveChildRecord(4242)
	// Only record lines count. A late line of another test can land in this capture.
	if out := logs.String(); strings.Contains(out, "[process.Registry]") {
		t.Errorf("these removals logged a record line, want none:\n%s", out)
	}
}

// TestRemoveRecordThroughChildrenSymlinkInsideRunDir pins a children entry that is a
// relative symlink to a folder inside the run dir. The daemon writes no record through
// it, so the end of the child removes nothing there and logs nothing. A file
// <pid>.json in the target stays. 89cb6289 removed it (Linux and macOS).
// c9125eb8 removes a record only while it holds the start that the daemon wrote.
// This case is claustrum's own, not measured on c9125eb8.
func TestRemoveRecordThroughChildrenSymlinkInsideRunDir(t *testing.T) {
	logs := captureLogBuf(t)
	m := &Records{}
	m.RunDir = t.TempDir()
	placed := filepath.Join(m.RunDir, "kids", "4242.json")
	if err := os.Mkdir(filepath.Dir(placed), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(placed, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("kids", filepath.Join(m.RunDir, "children")); err != nil {
		t.Fatal(err)
	}

	m.RemoveChildRecord(4242) // the file is there
	if _, err := os.Stat(placed); err != nil {
		t.Errorf("the placed file is gone (%v), want it kept", err)
	}
	m.RemoveChildRecord(4243) // no file for this pid

	if out := logs.String(); strings.Contains(out, "[process.Registry]") {
		t.Errorf("the removals logged a record line, want none:\n%s", out)
	}
	if fi, err := os.Lstat(filepath.Join(m.RunDir, "children")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the children symlink changed: %v, %v", fi, err)
	}
}

// TestRemoveRecordGoesThroughHeldRunDir pins the removal at the end of a child to the
// held os.Root of the run dir. After a rename of the run dir, the record under the new
// name goes and the old path is not made again (measured on Linux). A children symlink
// that leads out of the run dir is refused: the file in its target stays. 89cb6289
// logged the failed removal (measured on Linux). Here the daemon wrote no record, so
// it tries no removal and logs nothing (claustrum's own, not measured on c9125eb8).
func TestRemoveRecordGoesThroughHeldRunDir(t *testing.T) {
	t.Run("renamed run dir", func(t *testing.T) {
		parent := t.TempDir()
		d := filepath.Join(parent, "c1")
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
		m := &Records{RunDir: d}
		m.HoldRunDir()
		renamed := filepath.Join(parent, "c9")
		if err := os.Rename(d, renamed); err != nil {
			t.Fatal(err)
		}
		rec := filepath.Join(renamed, "children", "4242.json")
		m.writeRecord(childRecord{Pid: 4242, Start: "1", Argv0: "a"})
		if _, err := os.Stat(rec); err != nil {
			t.Fatalf("no record under the new name: %v", err)
		}

		m.RemoveChildRecord(4242)

		if _, err := os.Stat(rec); !os.IsNotExist(err) {
			t.Errorf("the record under the new name is still there (stat error: %v)", err)
		}
		if _, err := os.Lstat(d); !os.IsNotExist(err) {
			t.Errorf("the old run dir path exists again (lstat error: %v)", err)
		}
	})

	t.Run("children symlink out of the run dir", func(t *testing.T) {
		logs := captureLogBuf(t)
		parent := t.TempDir()
		d := filepath.Join(parent, "c1")
		outside := filepath.Join(parent, "outside")
		for _, dir := range []string{d, outside} {
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		placed := filepath.Join(outside, "4242.json")
		if err := os.WriteFile(placed, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(d, "children")); err != nil {
			t.Fatal(err)
		}
		m := &Records{RunDir: d}
		m.HoldRunDir()

		m.RemoveChildRecord(4242)

		if _, err := os.Stat(placed); err != nil {
			t.Errorf("the file outside the run dir is gone: %v", err)
		}
		if out := logs.String(); strings.Contains(out, "[process.Registry]") {
			t.Errorf("the removal logged a record line, want none:\n%s", out)
		}
	})
}
