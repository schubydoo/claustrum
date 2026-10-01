//go:build linux || darwin

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestLauncherChildRecordNamesProgram pins S12: under a run-shaped socket the child
// record of a launched spawn has argv0 = the launcher and a program field with the
// command, after argv0 and before start. S12 was VM-measured on Linux only. On
// darwin this test pins claustrum's own record format. A spawn without a launcher
// keeps the 19f30c46 record with no program field (claustrum's choice).
func TestLauncherChildRecordNamesProgram(t *testing.T) {
	base, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	runDir := filepath.Join(base, "run", "c1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, sock := newRunningServerAt(t, filepath.Join(runDir, "rpc.sock"))
	t.Cleanup(func() { s.procs.killAll() })
	cl := dial(t, sock)
	wrap, exe := wrapLauncherFixture(t)

	read := func(pid int) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(runDir, "children", strconv.Itoa(pid)+".json"))
		if err != nil {
			t.Fatalf("no child record: %v", err)
		}
		return string(b)
	}
	env := map[string]string{"CLAUSTRUM_TEST_HELPER": "wrap", "CLAUSTRUM_TEST_WRAP_NEXT": "sleep"}
	var reply struct {
		Result spawnResult `json:"result"`
	}
	raw := cl.call(launchedSpawnReq(t, map[string]any{"id": "p1", "command": exe, "args": []string{"30"}, "env": env, "launcher": []string{wrap}, "wantPid": true}))
	if err := json.Unmarshal(raw, &reply); err != nil || reply.Result.Pid == 0 {
		t.Fatalf("spawn reply %s: %v", raw, err)
	}
	rec := read(reply.Result.Pid)
	if want := `,"argv0":"` + wrap + `","program":"` + exe + `","start":"`; !strings.Contains(rec, want) {
		t.Errorf("launched record %s\nwant it to hold %s", rec, want)
	}

	sleep, sleepEnv := helperCommand(t, "sleep")
	raw = cl.call(launchedSpawnReq(t, map[string]any{"id": "p2", "command": sleep, "args": []string{"30"}, "env": sleepEnv, "wantPid": true}))
	if err := json.Unmarshal(raw, &reply); err != nil || reply.Result.Pid == 0 {
		t.Fatalf("spawn reply %s: %v", raw, err)
	}
	rec = read(reply.Result.Pid)
	if want := `,"argv0":"` + sleep + `","start":"`; !strings.Contains(rec, want) || strings.Contains(rec, `"program"`) {
		t.Errorf("plain record %s\nwant it to hold %s and no program field", rec, want)
	}
}
