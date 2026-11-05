//go:build linux

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/schubydoo/claustrum/internal/childrec"
)

// TestSocketSpawnWritesChildRecord is the end-to-end proof: a daemon on a
// run/<clientId>/rpc.sock socket spawns a child, and <runDir>/children/<pid>.json
// appears carrying that child's identity. A mutant that dropped the RecordChild call
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
	if s.procs.RunDir != runDir {
		t.Fatalf("procs.RunDir = %q, want %q", s.procs.RunDir, runDir)
	}
	cl := dial(t, sock)

	// A long-lived child, so /proc/<pid>/stat is readable when RecordChild runs.
	exe, env := helperCommand(t, "sleep")
	body, merr := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "1", "method": "process.spawn", "auth": testToken,
		"params": map[string]any{"id": "sleeper", "command": exe, "args": []string{"60"}, "env": env},
	})
	if merr != nil {
		t.Fatal(merr)
	}
	cl.call(string(body))
	t.Cleanup(func() { s.procs.KillAll() }) // reap the sleeper

	// RecordChild runs synchronously in spawn, so the record exists by the time the
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
	var rec childrec.RecordForTest
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("record not valid JSON: %v", err)
	}
	if ents[0].Name() != strconv.Itoa(rec.Pid)+".json" {
		t.Errorf("record file %q does not match its pid %d", ents[0].Name(), rec.Pid)
	}
	if rec.Instance != s.instanceID {
		t.Errorf("record instance = %q, want the daemon's %q", rec.Instance, s.instanceID)
	}
	if want := strconv.FormatInt(childrec.ProcStartTicksForTest(rec.Pid), 10); rec.Start != want {
		t.Errorf("record start = %q, want the live child's /proc start-ticks %q", rec.Start, want)
	}
	if rec.Argv0 != exe {
		t.Errorf("record argv0 = %q, want the spawned command %q", rec.Argv0, exe)
	}
}
