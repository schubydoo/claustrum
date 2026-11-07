//go:build linux || darwin

package childrec

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// identityRecord is a record in the Linux shape of row C12, with made-up values.
func identityRecord(pid int) childRecord {
	return childRecord{
		Pid:         pid,
		Node:        "00000000-0000-0000-0000-000000000000/pid:[1234567890]",
		Host:        "machine-id:0123456789abcdef0123456789abcdef",
		Instance:    "abababababababababababababababab",
		DaemonPid:   1000,
		DaemonStart: "5000",
		Argv0:       "/tmp/plain.sh",
		Start:       "5012",
		At:          1700000000000,
	}
}

// replaceRecord puts data at path by a rename over the file, as the C12 rows did.
func replaceRecord(t *testing.T, path string, data []byte) {
	t.Helper()
	tmp := path + ".other"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

// TestRemoveChildRecordOnlyOnIdentity pins the removal at the end of a child to the
// record that this daemon wrote. Measured on c9125eb8 (Linux and macOS rows C12): the
// untouched record goes, and so do the records with a changed instance, daemonStart
// or pid. A changed start, a start and daemonStart changed together, a text that is
// not JSON and a blank file stay. No case logs a line. The node, host, argv0 and
// program cases are claustrum's own, not measured.
func TestRemoveChildRecordOnlyOnIdentity(t *testing.T) {
	edit := func(old, new string) func(string) string {
		return func(s string) string { return strings.Replace(s, old, new, 1) }
	}
	for _, tc := range []struct {
		name    string
		rewrite func(string) string // nil: the file stays as written
		removed bool
		program string // the program of the written record, "" for none
	}{
		{"untouched", nil, true, ""},
		{"instance changed", edit(`"instance":"abab`, `"instance":"0000`), true, ""},
		{"daemonStart changed", edit(`"daemonStart":"5000"`, `"daemonStart":"111"`), true, ""},
		{"pid changed", edit(`{"pid":4242,`, `{"pid":4343,`), true, ""},
		{"start changed", edit(`"start":"5012"`, `"start":"999999999"`), false, ""},
		{"start and daemonStart changed", func(s string) string {
			return edit(`"start":"5012"`, `"start":"5012-OTHER"`)(edit(`"daemonStart":"5000"`, `"daemonStart":"5000-OTHER"`)(s))
		}, false, ""},
		{"not json", func(string) string { return "not json at all\n" }, false, ""},
		{"blank", func(string) string { return "\n" }, false, ""},
		{"node changed (claustrum's own)", edit(`"node":"0000`, `"node":"1111`), false, ""},
		{"host changed (claustrum's own)", edit(`"host":"machine-id:0123`, `"host":"machine-id:9999`), false, ""},
		{"argv0 changed (claustrum's own)", edit(`"argv0":"/tmp/plain.sh"`, `"argv0":"/tmp/other.sh"`), false, ""},
		{"program changed (claustrum's own)", edit(`"program":"/tmp/cli"`, `"program":"/tmp/other-cli"`), false, "/tmp/cli"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogBuf(t)
			m := &Records{RunDir: t.TempDir()}
			rec := identityRecord(4242)
			rec.Program = tc.program
			m.writeRecord(rec)
			path := filepath.Join(m.RunDir, "children", "4242.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := string(data)
			if tc.rewrite != nil {
				want = tc.rewrite(want)
				if want == string(data) {
					t.Fatal("the rewrite changed nothing")
				}
				replaceRecord(t, path, []byte(want))
			}

			m.RemoveChildRecord(4242)

			got, err := os.ReadFile(path)
			switch {
			case tc.removed && !os.IsNotExist(err):
				t.Errorf("the record is still there (read error %v), want it removed", err)
			case !tc.removed && (err != nil || string(got) != want):
				t.Errorf("the record = %q (read error %v), want it kept as %q", got, err, want)
			}
			if out := logs.String(); strings.Contains(out, "[process.Registry]") {
				t.Errorf("the removal logged a record line, want none:\n%s", out)
			}
		})
	}
}

// TestRemoveChildRecordKeepsOtherFiles pins the files that the removal leaves alone
// (claustrum's own, not measured): a record that this daemon did not write, a
// symlink to its record, a FIFO, and a file larger than maxRecordRead.
func TestRemoveChildRecordKeepsOtherFiles(t *testing.T) {
	t.Run("not written by this daemon", func(t *testing.T) {
		logs := captureLogBuf(t)
		m := &Records{RunDir: t.TempDir()}
		if err := writeChildRecord(m.RunDir, identityRecord(4242)); err != nil {
			t.Fatal(err)
		}
		m.RemoveChildRecord(4242)
		if _, err := os.Stat(filepath.Join(m.RunDir, "children", "4242.json")); err != nil {
			t.Errorf("the record of another writer is gone: %v", err)
		}
		if out := logs.String(); strings.Contains(out, "[process.Registry]") {
			t.Errorf("the removal logged a record line, want none:\n%s", out)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		m := &Records{RunDir: t.TempDir()}
		m.writeRecord(identityRecord(4242))
		path := filepath.Join(m.RunDir, "children", "4242.json")
		if err := os.Rename(path, path+".real"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("4242.json.real", path); err != nil {
			t.Fatal(err)
		}
		m.RemoveChildRecord(4242)
		if fi, err := os.Lstat(path); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("the symlink at the record name changed: %v, %v", fi, err)
		}
	})
	t.Run("fifo", func(t *testing.T) {
		m := &Records{RunDir: t.TempDir()}
		m.writeRecord(identityRecord(4242))
		path := filepath.Join(m.RunDir, "children", "4242.json")
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() { m.RemoveChildRecord(4242); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("the removal blocks on a FIFO")
		}
		if fi, err := os.Lstat(path); err != nil || fi.Mode()&os.ModeNamedPipe == 0 {
			t.Errorf("the FIFO at the record name changed: %v, %v", fi, err)
		}
	})
	t.Run("too large", func(t *testing.T) {
		m := &Records{RunDir: t.TempDir()}
		m.writeRecord(identityRecord(4242))
		path := filepath.Join(m.RunDir, "children", "4242.json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		big := append(data, []byte(strings.Repeat(" ", maxRecordRead))...)
		replaceRecord(t, path, big)
		m.RemoveChildRecord(4242)
		if fi, err := os.Stat(path); err != nil || fi.Size() != int64(len(big)) {
			t.Errorf("the large file changed: %v, %v", fi, err)
		}
	})
}

// TestRemoveChildRecordKeepsSwappedFile pins the last test before the remove
// (claustrum's own, not measured). A file that takes the record name after the read
// stays, even when it holds the same record.
func TestRemoveChildRecordKeepsSwappedFile(t *testing.T) {
	m := &Records{RunDir: t.TempDir()}
	m.writeRecord(identityRecord(4242))
	path := filepath.Join(m.RunDir, "children", "4242.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	old := recordCompared
	t.Cleanup(func() { recordCompared = old })
	recordCompared = func() { replaceRecord(t, path, data) }

	m.RemoveChildRecord(4242)

	if got, err := os.ReadFile(path); err != nil || string(got) != string(data) {
		t.Errorf("the swapped file = %q (read error %v), want it kept", got, err)
	}
}
