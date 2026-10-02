//go:build linux || darwin

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// runShapedServer boots the harness daemon on <base>/run/c1/rpc.sock, the socket shape
// that writes child records, and returns the server, a client and the run dir.
func runShapedServer(t *testing.T) (*server, *testClient, string) {
	t.Helper()
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
	return s, dial(t, sock), runDir
}

// spawnSleeper spawns the test binary as a 60 s sleeper under id and returns its pid.
// The sleeper lives until the test kills it, so its start value is readable when the
// daemon writes the record.
func spawnSleeper(t *testing.T, cl *testClient, id string) int {
	t.Helper()
	exe, env := helperCommand(t, "sleep")
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "process.spawn", "auth": testToken,
		"params": map[string]any{"id": id, "command": exe, "args": []string{"60"}, "env": env, "wantPid": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Result spawnResult `json:"result"`
	}
	raw := cl.call(string(body))
	if err := json.Unmarshal(raw, &reply); err != nil || reply.Result.Pid == 0 {
		t.Fatalf("spawn reply %s: %v", raw, err)
	}
	return reply.Result.Pid
}

// killChild ends the child id with process.kill and waits for its exit frame.
func killChild(t *testing.T, cl *testClient, id string) {
	t.Helper()
	cl.call(authed(`{"jsonrpc":"2.0","id":7,"method":"process.kill","params":{"id":"` + id + `"}}`))
	cl.waitExit(id)
}

func childrenNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

// TestChildEndRemovesRecord pins that the end of a child removes its record, and that
// the removal comes before the exit frame (89cb6289, Linux rows SP03a and SP06). The
// record exists while the child runs. When the exit frame is here, the record is gone.
func TestChildEndRemovesRecord(t *testing.T) {
	_, cl, runDir := runShapedServer(t)
	pid := spawnSleeper(t, cl, "c1")
	rec := filepath.Join(runDir, "children", strconv.Itoa(pid)+".json")
	if _, err := os.Stat(rec); err != nil {
		t.Fatalf("no record while the child runs: %v", err)
	}
	killChild(t, cl, "c1")
	if _, err := os.Stat(rec); !os.IsNotExist(err) {
		t.Errorf("the record is still there after the exit frame (stat error: %v)", err)
	}
}

// TestChildEndRemovesRecordBeforeDrain pins when the record goes: at the end of the
// child, not at the exit frame. The child leaves a grandchild on its stdout, so the
// exit frame waits for the whole drain grace (89cb6289, Linux row SP05: the record
// went 60 ms after the end of the child and the frame came 5 s later). It also pins
// the pipe-drain line and its place before the read-error line.
func TestChildEndRemovesRecordBeforeDrain(t *testing.T) {
	old := exitDrainGrace
	exitDrainGrace = 8 * time.Second
	t.Cleanup(func() { exitDrainGrace = old })
	logs := captureLogBuf(t)
	_, cl, runDir := runShapedServer(t)

	// The helper starts a grandchild that holds its stdout, writes the pid of the
	// grandchild to a file, and then stays alive until the test ends it.
	pidFile := filepath.Join(t.TempDir(), "kid.pid")
	exe, env := helperCommand(t, "tree-stdout")
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "process.spawn", "auth": testToken,
		"params": map[string]any{"id": "c1", "command": exe, "args": []string{pidFile}, "env": env, "wantPid": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Result spawnResult `json:"result"`
	}
	raw := cl.call(string(body))
	if err := json.Unmarshal(raw, &reply); err != nil || reply.Result.Pid == 0 {
		t.Fatalf("spawn reply %s: %v", raw, err)
	}
	kid := 0
	for deadline := time.Now().Add(10 * time.Second); kid == 0; time.Sleep(5 * time.Millisecond) {
		if b, err := os.ReadFile(pidFile); err == nil {
			kid, _ = strconv.Atoi(string(b))
		}
		if time.Now().After(deadline) {
			t.Fatal("the helper never started its grandchild")
		}
	}
	// The grandchild is the test's own fixture. End it by its pid when the test ends.
	t.Cleanup(func() { _ = syscall.Kill(kid, syscall.SIGKILL) })
	rec := filepath.Join(runDir, "children", strconv.Itoa(reply.Result.Pid)+".json")
	if _, err := os.Stat(rec); err != nil {
		t.Fatalf("no record while the child runs: %v", err)
	}

	// End the child alone, by its pid. The grandchild keeps the pipe open.
	if err := syscall.Kill(reply.Result.Pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill the helper: %v", err)
	}
	// Half of the grace is the limit. A removal that waits for the drain comes later.
	gone := false
	for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(rec); os.IsNotExist(err) {
			gone = true
			break
		}
	}
	cl.mu.Lock()
	exited := false
	for _, f := range cl.fr {
		exited = exited || f.Stream == "exit"
	}
	cl.mu.Unlock()
	if !gone {
		t.Error("the record was still there 4 s after the end of the child, inside an 8 s drain")
	}
	if exited {
		t.Error("the exit frame came before the check, so the row does not show the order")
	}
	// The exit frame comes at the end of the 8 s drain, later than the wait of waitExit.
	for deadline := time.Now().Add(30 * time.Second); !exited; time.Sleep(20 * time.Millisecond) {
		cl.mu.Lock()
		for _, f := range cl.fr {
			exited = exited || f.Stream == "exit"
		}
		cl.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("no exit frame after the drain grace")
		}
	}
	out := logs.String()
	drain := strings.Index(out, "[process.Manager] Process c1: pipe drain grace expired (grandchild holding stdio?); force-closing\n")
	readErr := strings.Index(out, "[process.Manager] stdout read error for process c1")
	if drain < 0 || readErr < 0 || drain > readErr {
		t.Errorf("want the pipe-drain line and then the stdout read-error line, got:\n%s", out)
	}
}

// TestShutdownRemovesChildRecords pins that a graceful shutdown leaves no record: the
// daemon waits for the children it killed, and the end of each one removes its record
// (89cb6289, Linux row RP12a: children/ is empty after a -stop).
func TestShutdownRemovesChildRecords(t *testing.T) {
	// The wait ends as soon as both children are reaped. A wide bound keeps a slow
	// runner from ending it first.
	old := shutdownRecordWait
	shutdownRecordWait = 30 * time.Second
	t.Cleanup(func() { shutdownRecordWait = old })
	s, cl, runDir := runShapedServer(t)
	spawnSleeper(t, cl, "c1")
	spawnSleeper(t, cl, "c2")
	if n := len(childrenNames(t, filepath.Join(runDir, "children"))); n != 2 {
		t.Fatalf("%d records before the shutdown, want 2", n)
	}
	s.stopChildren()
	if names := childrenNames(t, filepath.Join(runDir, "children")); len(names) != 0 {
		t.Errorf("records left after the shutdown step: %v", names)
	}
}

// TestRecordCallsWithNoUsableRunDir covers the record calls of a manager whose run dir
// cannot be opened, and the two gates of the removal. A write that cannot open the
// run dir logs the cannot-record line. A removal there, a removal with no run dir,
// and a removal for a pid below 2 do nothing and log nothing.
func TestRecordCallsWithNoUsableRunDir(t *testing.T) {
	logs := captureLogBuf(t)
	m := newTestProcManager(t)
	m.runDir = filepath.Join(t.TempDir(), "gone")
	m.writeRecord(childRecord{Pid: 4242, Start: "1", Argv0: "a"})
	if want := "[process.Registry] cannot record child 4242: "; !strings.Contains(logs.String(), want) {
		t.Errorf("log = %q\nwant it to hold %q", logs.String(), want)
	}
	logs.Reset()
	m.removeChildRecord(4242)
	newTestProcManager(t).removeChildRecord(4242)

	held := newTestProcManager(t)
	held.runDir = t.TempDir()
	if err := writeChildRecord(held.runDir, childRecord{Pid: 1, Start: "1", Argv0: "a"}); err != nil {
		t.Fatal(err)
	}
	held.removeChildRecord(1)
	if _, err := os.Stat(filepath.Join(held.runDir, "children", "1.json")); err != nil {
		t.Errorf("the removal acted for a pid below 2: %v", err)
	}
	// A record that is already gone is not a failed removal.
	held.removeChildRecord(4242)
	// Only record lines count. A late line of another test can land in this capture.
	if out := logs.String(); strings.Contains(out, "[process.Registry]") {
		t.Errorf("these removals logged a record line, want none:\n%s", out)
	}
}

// TestRemoveRecordThroughChildrenSymlinkInsideRunDir pins the removal at the end of a
// child when children is a relative symlink to a folder inside the run dir. The
// removal goes through the link, as on 89cb6289. A file <pid>.json in the target is
// removed (Linux row SLb, macOS row KDb). With no such file, nothing is removed and no
// line is logged (Linux row SLa, macOS row KDa). A symlink that leads out of the run
// dir stays refused: TestChildrenNotADirectory pins that (row SP07b).
func TestRemoveRecordThroughChildrenSymlinkInsideRunDir(t *testing.T) {
	logs := captureLogBuf(t)
	m := newTestProcManager(t)
	m.runDir = t.TempDir()
	placed := filepath.Join(m.runDir, "kids", "4242.json")
	if err := os.Mkdir(filepath.Dir(placed), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(placed, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("kids", filepath.Join(m.runDir, "children")); err != nil {
		t.Fatal(err)
	}

	m.removeChildRecord(4242) // the file is there
	if _, err := os.Stat(placed); !os.IsNotExist(err) {
		t.Errorf("the placed file is still there (stat error: %v), want it removed through the link", err)
	}
	m.removeChildRecord(4243) // no file for this pid

	if out := logs.String(); strings.Contains(out, "[process.Registry]") {
		t.Errorf("the removals logged a record line, want none:\n%s", out)
	}
	if fi, err := os.Lstat(filepath.Join(m.runDir, "children")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the children symlink changed: %v, %v", fi, err)
	}
}

// TestKeepChildrenSkipsShutdownWait pins that a shutdown with -keep-children does not
// wait for the records. The children stay alive, so their records stay, and a wait for
// them takes the whole bound. The bound is 30 s here, and the call returns at once.
func TestKeepChildrenSkipsShutdownWait(t *testing.T) {
	old := shutdownRecordWait
	shutdownRecordWait = 30 * time.Second
	t.Cleanup(func() { shutdownRecordWait = old })
	s, cl, runDir := runShapedServer(t)
	s.keepChildren = true
	t.Cleanup(s.procs.killAll) // the kept children must end with the test
	pids := []int{spawnSleeper(t, cl, "c1"), spawnSleeper(t, cl, "c2")}

	done := make(chan struct{})
	go func() { defer close(done); s.stopChildren() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		// End the test's own children, so the wait and the test end.
		s.procs.killAll()
		<-done
		t.Fatal("the shutdown with -keep-children waited for the records")
	}
	if n := len(childrenNames(t, filepath.Join(runDir, "children"))); n != 2 {
		t.Errorf("%d records after the shutdown step, want the 2 records of the kept children", n)
	}
	for _, pid := range pids {
		if err := syscall.Kill(pid, 0); err != nil {
			t.Errorf("kept child %d is gone: %v", pid, err)
		}
	}
}

// TestAwaitRecordsRemovedIsBounded pins the bound of the shutdown wait: a child whose
// exit goroutine never reports does not hold the daemon past shutdownRecordWait.
func TestAwaitRecordsRemovedIsBounded(t *testing.T) {
	old := shutdownRecordWait
	shutdownRecordWait = 50 * time.Millisecond
	t.Cleanup(func() { shutdownRecordWait = old })
	m := newTestProcManager(t)
	m.runDir = t.TempDir()
	m.procs["stuck"] = &managedProc{id: "stuck", recordDone: make(chan struct{})}
	m.procs["bare"] = &managedProc{id: "bare"} // no channel: nothing to wait for
	done := make(chan struct{})
	go func() { defer close(done); m.awaitRecordsRemoved() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the shutdown wait did not end at its bound")
	}
}

// TestChildrenNotADirectory pins a children entry that is not a real folder: no record
// is written, nothing is written through a symlink, and the two log lines come in
// order (89cb6289, Linux rows SP07a and SP07b). The removal at the end of the child
// is still tried, and its failure is logged before the exit line.
func TestChildrenNotADirectory(t *testing.T) {
	for _, tc := range []struct {
		name    string
		plant   func(t *testing.T, runDir, outside string)
		removal string
	}{
		{"regular file", func(t *testing.T, runDir, _ string) {
			if err := os.WriteFile(filepath.Join(runDir, "children"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "not a directory"},
		{"symlink to a folder outside the run dir", func(t *testing.T, runDir, outside string) {
			if err := os.Symlink(outside, filepath.Join(runDir, "children")); err != nil {
				t.Fatal(err)
			}
		}, "path escapes from parent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogBuf(t)
			_, cl, runDir := runShapedServer(t)
			outside := filepath.Join(filepath.Dir(filepath.Dir(runDir)), "outside")
			if err := os.Mkdir(outside, 0o700); err != nil {
				t.Fatal(err)
			}
			tc.plant(t, runDir, outside)
			before, err := os.Lstat(filepath.Join(runDir, "children"))
			if err != nil {
				t.Fatal(err)
			}

			pid := strconv.Itoa(spawnSleeper(t, cl, "c1"))
			killChild(t, cl, "c1")

			if names := childrenNames(t, outside); len(names) != 0 {
				t.Errorf("the daemon wrote through the children entry: %v", names)
			}
			after, err := os.Lstat(filepath.Join(runDir, "children"))
			if err != nil || after.Mode() != before.Mode() || after.Size() != before.Size() {
				t.Errorf("the children entry changed: before %v, after %v (%v)", before.Mode(), after, err)
			}
			want := []string{
				"[process.Manager] Process c1 started, PID=" + pid + ",",
				"[process.Registry] " + runDir + "/children is not a directory; recording nothing\n",
				"[process.Registry] cannot remove record of child " + pid + ": removeat children/" + pid + ".json: " + tc.removal + "\n",
				"[process.Manager] Process c1 exited with code",
			}
			out, at := logs.String(), 0
			for _, w := range want {
				i := strings.Index(out[at:], w)
				if i < 0 {
					t.Fatalf("log line %q is absent or out of order in:\n%s", w, out)
				}
				at += i + len(w)
			}
		})
	}
}

// TestChildrenMode000 pins a children folder that refuses every entry: no record, the
// cannot-record line with the temp name, and the cannot-remove line at the end of the
// child (89cb6289, Linux row SP07c).
func TestChildrenMode000(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the mode of a folder")
	}
	logs := captureLogBuf(t)
	_, cl, runDir := runShapedServer(t)
	children := filepath.Join(runDir, "children")
	if err := os.Mkdir(children, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(children, 0o700) })

	pid := strconv.Itoa(spawnSleeper(t, cl, "c1"))
	killChild(t, cl, "c1")

	if err := os.Chmod(children, 0o700); err != nil {
		t.Fatal(err)
	}
	if names := childrenNames(t, children); len(names) != 0 {
		t.Errorf("entries in a children folder of mode 000: %v", names)
	}
	out := logs.String()
	record := regexp.MustCompile(`\[process\.Registry\] cannot record child ` + pid + `: openat children/\.rec-` + pid + `\.json\.[0-9a-z]{12}: permission denied\n`)
	remove := "[process.Registry] cannot remove record of child " + pid + ": removeat children/" + pid + ".json: permission denied\n"
	loc := record.FindStringIndex(out)
	if loc == nil || !strings.Contains(out[loc[1]:], remove) {
		t.Errorf("want the cannot-record line and then the cannot-remove line, got:\n%s", out)
	}
}

// TestRecordFollowsRenamedRunDir pins a run dir that is renamed while the daemon runs:
// the record goes under the new name, the old path is not made again, and the end of
// the child still removes the record (89cb6289, Linux row SP08).
func TestRecordFollowsRenamedRunDir(t *testing.T) {
	_, cl, runDir := runShapedServer(t)
	renamed := filepath.Join(filepath.Dir(runDir), "c9")
	if err := os.Rename(runDir, renamed); err != nil {
		t.Fatal(err)
	}
	pid := spawnSleeper(t, cl, "c1")
	rec := filepath.Join(renamed, "children", strconv.Itoa(pid)+".json")
	if _, err := os.Stat(rec); err != nil {
		t.Errorf("no record under the new name of the run dir: %v", err)
	}
	if _, err := os.Lstat(runDir); !os.IsNotExist(err) {
		t.Errorf("the old run dir path exists again (lstat error: %v)", err)
	}
	killChild(t, cl, "c1")
	if _, err := os.Stat(rec); !os.IsNotExist(err) {
		t.Errorf("the record under the new name is still there after the exit frame (stat error: %v)", err)
	}
}
