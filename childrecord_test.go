package main

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// writeChildRecord writes rec below runDir the way the daemon does: through an os.Root
// of the run dir. The daemon holds that root open for its whole life. A test opens it
// for the one write.
func writeChildRecord(runDir string, rec childRecord) error {
	root, err := os.OpenRoot(runDir)
	if err != nil {
		return err
	}
	defer root.Close()
	return writeChildRecordIn(root, rec)
}

// TestChildRecordTempName pins the temp name of a record: ".rec-<pid>.json." and 12
// chars, inside children/ (89cb6289, Linux row SP06). Before, the name was
// "<pid>.<19 digits>.json". The two shapes differ at a daemon start: a leftover of
// the old shape ends in ".json", so the reap reads it.
//
// The test reads the name that the write really used, in two ways: through the seam,
// and in the error of a write that cannot make its temp file.
func TestChildRecordTempName(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a read-only folder that refuses a create, as a user that is not root")
	}
	runDir := t.TempDir()
	children := filepath.Join(runDir, "children")
	old := childRecordTempName
	t.Cleanup(func() { childRecordTempName = old })
	var used string
	childRecordTempName = func(pid int) string { used = old(pid); return used }

	if err := writeChildRecord(runDir, childRecord{Pid: 4242, Start: "1", Argv0: "a"}); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^children/\.rec-4242\.json\.[0-9a-z]{12}$`).MatchString(used) {
		t.Errorf("temp name = %q, want children/.rec-4242.json.<12 chars of 0-9a-z>", used)
	}
	// A second write whose temp file cannot be made names that temp file in its error.
	if err := os.Chmod(children, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(children, 0o700) })
	err := writeChildRecord(runDir, childRecord{Pid: 4242, Start: "1", Argv0: "a"})
	if err == nil || !regexp.MustCompile(`^openat children/\.rec-4242\.json\.[0-9a-z]{12}: permission denied$`).MatchString(err.Error()) {
		t.Errorf("error = %v, want openat children/.rec-4242.json.<12 chars>: permission denied", err)
	}
}

// TestWriteChildRecord pins the on-disk childRecord contract: the exact field ORDER
// and the string-vs-number typing measured byte-for-byte against 19f30c46 (daemonStart
// and start are tick STRINGS; pid, daemonPid and at are numbers). A reordered struct
// field or a string/number swap changes these bytes and fails here. It also checks the
// write is atomic (no temp file left behind) and the children dir is 0700.
func TestWriteChildRecord(t *testing.T) {
	runDir := t.TempDir()
	rec := childRecord{
		Pid:         4242,
		Node:        "boot-uuid/pid:[4026531836]",
		Host:        "machine-id:0123456789abcdef0123456789abcdef",
		Instance:    "fedcba9876543210fedcba9876543210",
		DaemonPid:   99,
		DaemonStart: "111",
		Argv0:       "/bin/sleep",
		Start:       "222",
		At:          1737000000000,
	}
	if err := writeChildRecord(runDir, rec); err != nil {
		t.Fatalf("writeChildRecord: %v", err)
	}
	const want = `{"pid":4242,"node":"boot-uuid/pid:[4026531836]","host":"machine-id:0123456789abcdef0123456789abcdef","instance":"fedcba9876543210fedcba9876543210","daemonPid":99,"daemonStart":"111","argv0":"/bin/sleep","start":"222","at":1737000000000}`
	got, err := os.ReadFile(filepath.Join(runDir, "children", "4242.json"))
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	if string(got) != want {
		t.Errorf("childRecord bytes:\n got=%s\nwant=%s", got, want)
	}
	// Atomic: the rename leaves only the final file, no temp.
	ents, err := os.ReadDir(filepath.Join(runDir, "children"))
	if err != nil {
		t.Fatalf("read children dir: %v", err)
	}
	for _, e := range ents {
		if e.Name() != "4242.json" {
			t.Errorf("leftover file in children dir: %q (temp not renamed/cleaned)", e.Name())
		}
	}
	// The children dir is created 0700 (POSIX; Windows perms differ).
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(runDir, "children"))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o700 {
			t.Errorf("children dir mode = %o, want 700", fi.Mode().Perm())
		}
	}
}

// TestWriteChildRecordErrors covers the reachable failure paths of the atomic write.
// Two need no permissions deny (which is unreliable under a root CI leg): the children
// path is a regular file, and the rename fails because the final <pid>.json path is a
// directory. The third is a run dir that refuses the mkdir of children, and it skips
// as root. The remaining error branches (json.Marshal of this plain struct, and the
// write to a freshly-made temp) do not fail on any input a test can honestly produce.
func TestWriteChildRecordErrors(t *testing.T) {
	rec := childRecord{Pid: 4242, Node: "n", Host: "h", Instance: "i", DaemonPid: 1, DaemonStart: "1", Argv0: "a", Start: "2", At: 3}

	t.Run("mkdir children fails", func(t *testing.T) {
		runDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(runDir, "children"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := writeChildRecord(runDir, rec); err == nil {
			t.Error("writeChildRecord succeeded though children/ could not be made (it is a file)")
		}
	})

	t.Run("children cannot be made in a read-only run dir", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("needs a read-only folder that refuses a create, as a user that is not root")
		}
		runDir := t.TempDir()
		if err := os.Chmod(runDir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(runDir, 0o700) })
		err := writeChildRecord(runDir, rec)
		if err == nil || errors.Is(err, errChildrenNotDir) || !strings.Contains(err.Error(), "children: permission denied") {
			t.Errorf("error = %v, want the refused mkdir of children", err)
		}
	})

	t.Run("rename fails, temp cleaned", func(t *testing.T) {
		runDir := t.TempDir()
		final := filepath.Join(runDir, "children", strconv.Itoa(rec.Pid)+".json")
		if err := os.MkdirAll(final, 0o700); err != nil { // a directory where the record file should go
			t.Fatal(err)
		}
		if err := writeChildRecord(runDir, rec); err == nil {
			t.Error("writeChildRecord succeeded though the final path is a directory")
		}
		// Only the pre-made directory remains: the temp file was cleaned up on failure.
		ents, err := os.ReadDir(filepath.Join(runDir, "children"))
		if err != nil {
			t.Fatal(err)
		}
		if len(ents) != 1 || ents[0].Name() != strconv.Itoa(rec.Pid)+".json" {
			t.Errorf("temp not cleaned after failed rename: %v", ents)
		}
	})
}
