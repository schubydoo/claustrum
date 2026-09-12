package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

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

// TestWriteChildRecordErrors covers the two reachable failure paths of the atomic
// write, without a permissions/chmod deny (which is unreliable under a root CI leg):
// the children dir cannot be made because that path is a regular file, and the rename
// fails because the final <pid>.json path is a directory. The remaining error branches
// (json.Marshal of this plain struct, and os.WriteFile to a freshly-made temp) do not
// fail on any input a test can honestly produce.
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
