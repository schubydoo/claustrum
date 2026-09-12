//go:build linux

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestMachineID pins the /etc/machine-id -> /var/lib/dbus/machine-id fallback
// (reference build 19f30c46): the first readable non-empty file wins, the second is
// used when the first is missing, and "" is returned when neither is readable. Seams
// machineIDPaths at temp files so it does not depend on the host's real id.
func TestMachineID(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	old := machineIDPaths
	t.Cleanup(func() { machineIDPaths = old })
	machineIDPaths = []string{a, b}

	if got := machineID(); got != "" {
		t.Errorf("neither file: machineID = %q, want \"\"", got)
	}
	if err := os.WriteFile(b, []byte("bbbb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := machineID(); got != "bbbb" {
		t.Errorf("fallback only: machineID = %q, want \"bbbb\"", got)
	}
	if err := os.WriteFile(a, []byte("  aaaa\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := machineID(); got != "aaaa" {
		t.Errorf("primary present: machineID = %q, want \"aaaa\"", got)
	}
}

// TestProcStartTicks proves procStartTicks reads field 22 of /proc/<pid>/stat and
// agrees with ownStartTicks for this process, and returns 0 for a pid it cannot read.
func TestProcStartTicks(t *testing.T) {
	self := procStartTicks(os.Getpid())
	if self <= 0 {
		t.Fatalf("procStartTicks(self) = %d, want > 0", self)
	}
	if own := ownStartTicks(); self != own {
		t.Errorf("procStartTicks(self) = %d != ownStartTicks() = %d", self, own)
	}
	if got := procStartTicks(1 << 30); got != 0 {
		t.Errorf("procStartTicks(nonexistent) = %d, want 0", got)
	}
}

// TestRecordChild drives recordChild for a live pid (this process) and asserts the
// written record's fields, then confirms the three gates (no run dir, pid < 2, and an
// unreadable start-time) all skip the write.
func TestRecordChild(t *testing.T) {
	runDir := t.TempDir()
	m := newTestProcManager(t)
	m.runDir = runDir
	m.instanceID = "inst-32hex-abc"

	m.recordChild(os.Getpid(), "/bin/sleep")
	data, err := os.ReadFile(filepath.Join(runDir, "children", strconv.Itoa(os.Getpid())+".json"))
	if err != nil {
		t.Fatalf("record not written: %v", err)
	}
	var rec childRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("record not valid JSON: %v", err)
	}
	if rec.Pid != os.Getpid() {
		t.Errorf("pid = %d, want %d", rec.Pid, os.Getpid())
	}
	if rec.Instance != "inst-32hex-abc" {
		t.Errorf("instance = %q, want the daemon instanceID", rec.Instance)
	}
	if rec.DaemonPid != os.Getpid() {
		t.Errorf("daemonPid = %d, want %d", rec.DaemonPid, os.Getpid())
	}
	if want := strconv.FormatInt(ownStartTicks(), 10); rec.DaemonStart != want {
		t.Errorf("daemonStart = %q, want %q", rec.DaemonStart, want)
	}
	if want := strconv.FormatInt(procStartTicks(os.Getpid()), 10); rec.Start != want {
		t.Errorf("start = %q, want the child's /proc start-ticks %q", rec.Start, want)
	}
	if rec.Argv0 != "/bin/sleep" {
		t.Errorf("argv0 = %q, want /bin/sleep", rec.Argv0)
	}
	if rec.At <= 0 {
		t.Errorf("at = %d, want an epoch-ms timestamp > 0", rec.At)
	}
	if rec.Node == "" {
		t.Errorf("node is empty; want nodeID()'s boot-id/pidns")
	}
	if !strings.HasPrefix(rec.Host, "machine-id:") {
		t.Errorf("host = %q, want a machine-id: prefix", rec.Host)
	}

	// Gate: no run dir -> no write (and no panic).
	newTestProcManager(t).recordChild(os.Getpid(), "/bin/sleep")

	// Gate: pid < 2 -> skip.
	m.recordChild(1, "/sbin/init")
	if _, err := os.Stat(filepath.Join(runDir, "children", "1.json")); !os.IsNotExist(err) {
		t.Errorf("pid 1 was recorded; the pid<2 gate must skip it")
	}

	// Gate: unreadable start-time (a dead/bogus pid) -> skip.
	bogus := 1 << 30
	m.recordChild(bogus, "/x")
	if _, err := os.Stat(filepath.Join(runDir, "children", strconv.Itoa(bogus)+".json")); !os.IsNotExist(err) {
		t.Errorf("a pid with no readable start-time was recorded; the start==0 gate must skip it")
	}

	// A write failure is logged, not fatal: with the children path a file,
	// writeChildRecord fails and recordChild takes its error-log branch (no panic, no
	// record written).
	badDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(badDir, "children"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	mBad := newTestProcManager(t)
	mBad.runDir = badDir
	mBad.instanceID = "x"
	mBad.recordChild(os.Getpid(), "/bin/sleep")
	if _, err := os.Stat(filepath.Join(badDir, "children", strconv.Itoa(os.Getpid())+".json")); err == nil {
		t.Error("a record was written despite the write failing")
	}
}

// TestSocketSpawnWritesChildRecord is the end-to-end proof: a daemon on a
// run/<clientId>/rpc.sock socket spawns a child, and <runDir>/children/<pid>.json
// appears carrying that child's identity. A mutant that dropped the recordChild call
// in spawn would leave the children dir empty.
func TestSocketSpawnWritesChildRecord(t *testing.T) {
	base, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	runDir := filepath.Join(base, "run", "c0ffee02")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(runDir, "rpc.sock")
	s, _ := newRunningServerAt(t, sock)
	if s.procs.runDir != runDir {
		t.Fatalf("procs.runDir = %q, want %q", s.procs.runDir, runDir)
	}
	cl := dial(t, sock)

	// A long-lived child, so /proc/<pid>/stat is readable when recordChild runs.
	exe, env := helperCommand(t, "sleep")
	body, merr := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "1", "method": "process.spawn", "auth": testToken,
		"params": map[string]any{"id": "sleeper", "command": exe, "args": []string{"60"}, "env": env},
	})
	if merr != nil {
		t.Fatal(merr)
	}
	cl.call(string(body))
	t.Cleanup(func() { s.procs.killAll() }) // reap the sleeper

	// recordChild runs synchronously in spawn, so the record exists by the time the
	// spawn reply returned. Read the sole record in the children dir.
	ents, err := os.ReadDir(filepath.Join(runDir, "children"))
	if err != nil {
		t.Fatalf("children dir not created: %v", err)
	}
	if len(ents) != 1 {
		t.Fatalf("children dir has %d entries, want exactly 1 record", len(ents))
	}
	data, err := os.ReadFile(filepath.Join(runDir, "children", ents[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var rec childRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("record not valid JSON: %v", err)
	}
	if ents[0].Name() != strconv.Itoa(rec.Pid)+".json" {
		t.Errorf("record file %q does not match its pid %d", ents[0].Name(), rec.Pid)
	}
	if rec.Instance != s.instanceID {
		t.Errorf("record instance = %q, want the daemon's %q", rec.Instance, s.instanceID)
	}
	if want := strconv.FormatInt(procStartTicks(rec.Pid), 10); rec.Start != want {
		t.Errorf("record start = %q, want the live child's /proc start-ticks %q", rec.Start, want)
	}
	if rec.Argv0 != exe {
		t.Errorf("record argv0 = %q, want the spawned command %q", rec.Argv0, exe)
	}
}
