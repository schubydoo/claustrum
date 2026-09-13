//go:build darwin

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestRecordChildDarwin drives recordChild with the darwin identity helpers seamed to
// fixed values and asserts the written record carries the darwin-specific identity
// (node = boot-session UUID, host = the bare hostname, start/daemonStart = the UTC
// ANSIC start-time string), in the same field order as every other platform, and that
// the three skip gates (no run dir, pid < 2, unreadable start-time) hold. The helpers
// are seamed so the test never shells out to ps or reads the host's real identity.
func TestRecordChildDarwin(t *testing.T) {
	oldBoot, oldHost, oldStart := bootSessionUUID, darwinHostname, darwinProcStart
	t.Cleanup(func() { bootSessionUUID, darwinHostname, darwinProcStart = oldBoot, oldHost, oldStart })
	bootSessionUUID = func() string { return "6DB2FCC7-9427-4A74-BA07-7955DE0C597C" }
	darwinHostname = func() string { return "test-host.local" }
	const startStr = "Sun Sep 13 07:56:41 2026"
	starts := map[int]string{os.Getpid(): startStr} // any other pid -> "" (unreadable)
	darwinProcStart = func(pid int) string { return starts[pid] }

	runDir := t.TempDir()
	m := newTestProcManager(t)
	m.runDir = runDir
	m.instanceID = "d39d31249f6771a7855a7b81d23e5d0c"

	m.recordChild(os.Getpid(), "/bin/sleep")
	data, err := os.ReadFile(filepath.Join(runDir, "children", strconv.Itoa(os.Getpid())+".json"))
	if err != nil {
		t.Fatalf("record not written: %v", err)
	}

	// Field ORDER is the on-disk contract (shared cross-platform struct) — assert the
	// raw key sequence, since a darwin build must not reorder it.
	wantOrder := []string{"pid", "node", "host", "instance", "daemonPid", "daemonStart", "argv0", "start", "at"}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.Token() // opening {
	var gotOrder []string
	for i := 0; i < len(wantOrder); i++ {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		gotOrder = append(gotOrder, tok.(string))
		dec.Token() // value
	}
	if strings.Join(gotOrder, ",") != strings.Join(wantOrder, ",") {
		t.Errorf("field order = %v, want %v", gotOrder, wantOrder)
	}

	var rec childRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("record not valid JSON: %v", err)
	}
	if rec.Node != "6DB2FCC7-9427-4A74-BA07-7955DE0C597C" {
		t.Errorf("node = %q, want the boot-session UUID", rec.Node)
	}
	if rec.Host != "test-host.local" {
		t.Errorf("host = %q, want the bare hostname (no machine-id: prefix)", rec.Host)
	}
	if rec.Start != startStr {
		t.Errorf("start = %q, want the UTC ANSIC start-time %q", rec.Start, startStr)
	}
	if rec.DaemonStart != startStr {
		t.Errorf("daemonStart = %q, want %q", rec.DaemonStart, startStr)
	}
	if rec.Pid != os.Getpid() || rec.DaemonPid != os.Getpid() {
		t.Errorf("pid/daemonPid = %d/%d, want %d", rec.Pid, rec.DaemonPid, os.Getpid())
	}
	if rec.Instance != "d39d31249f6771a7855a7b81d23e5d0c" {
		t.Errorf("instance = %q, want the daemon instanceID", rec.Instance)
	}
	if rec.Argv0 != "/bin/sleep" {
		t.Errorf("argv0 = %q, want /bin/sleep", rec.Argv0)
	}
	if rec.At <= 0 {
		t.Errorf("at = %d, want an epoch-ms timestamp > 0", rec.At)
	}

	// Gate: no run dir -> no write, no panic.
	newTestProcManager(t).recordChild(os.Getpid(), "/bin/sleep")
	// Gate: pid < 2 -> skip.
	m.recordChild(1, "/sbin/launchd")
	if _, err := os.Stat(filepath.Join(runDir, "children", "1.json")); !os.IsNotExist(err) {
		t.Error("pid 1 was recorded; the pid<2 gate must skip it")
	}
	// Gate: unreadable start-time (a pid darwinProcStart returns "" for) -> skip.
	bogus := 1 << 30
	m.recordChild(bogus, "/x")
	if _, err := os.Stat(filepath.Join(runDir, "children", strconv.Itoa(bogus)+".json")); !os.IsNotExist(err) {
		t.Error("a pid with no readable start-time was recorded; the start=='' gate must skip it")
	}
}
