//go:build linux || darwin

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/childrec"
	"github.com/schubydoo/claustrum/internal/procinfo"
	"github.com/schubydoo/claustrum/internal/rpc"
	"github.com/schubydoo/claustrum/internal/supervisor"
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
		Result rpc.SpawnResult `json:"result"`
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
// the removal comes before the exit frame (`89cb6289`, measured on Linux). The record
// exists while the child runs. When the exit frame is here, the record is gone.
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

// TestSameIDKeepsTheOldProcessAndItsRecord pins a second spawn with the id of a child
// that still runs (`89cb6289`, measured on Linux and macOS). The first child gets no
// signal and its record stays beside the record of the second. A process.kill of the
// id ends the second child only, and only its record goes.
func TestSameIDKeepsTheOldProcessAndItsRecord(t *testing.T) {
	// The seam counts the signals to the first child. It is set before the daemon
	// starts, so no goroutine of the daemon reads the seam while the test writes it.
	var firstProc atomic.Pointer[os.Process]
	var toFirst atomic.Int32
	realSignal := supervisor.SignalGroupForTest()
	supervisor.SetSignalGroupForTest(func(g *supervisor.ProcGroup, proc *os.Process, signame string) error {
		if proc == firstProc.Load() {
			toFirst.Add(1)
		}
		return realSignal(g, proc, signame)
	})
	t.Cleanup(func() { supervisor.SetSignalGroupForTest(realSignal) })

	s, cl, runDir := runShapedServer(t)
	first := spawnSleeper(t, cl, "c1")
	old := s.procs.Get("c1")
	if old == nil {
		t.Fatal("the first spawn is not in the table")
	}
	firstProc.Store(old.CmdForTest().Process)
	// The daemon no longer ends the first child, so the test ends its own child and
	// waits for its exit line.
	t.Cleanup(func() { endOwnChild(t, old) })
	second := spawnSleeper(t, cl, "c1")
	rec := func(pid int) string { return filepath.Join(runDir, "children", strconv.Itoa(pid)+".json") }
	for _, pid := range []int{first, second} {
		if _, err := os.Stat(rec(pid)); err != nil {
			t.Errorf("no record of child %d after the second spawn: %v", pid, err)
		}
	}
	// A kill of the old process goes out inside the second spawn, before its reply. So
	// the count is final here, and no pause is needed.
	if n := toFirst.Load(); n != 0 {
		t.Fatalf("the second spawn sent %d signal(s) to the first child, want none", n)
	}
	if !childrec.ReadsAliveForTest(first) {
		t.Fatalf("the first child %d is not alive after the second spawn", first)
	}

	killChild(t, cl, "c1")
	if _, err := os.Stat(rec(second)); !os.IsNotExist(err) {
		t.Errorf("the record of the second child is still there after its end (stat error: %v)", err)
	}
	if _, err := os.Stat(rec(first)); err != nil {
		t.Errorf("the record of the first child went with the kill of the second: %v", err)
	}
	if n := toFirst.Load(); n != 0 {
		t.Errorf("the kill of the id sent %d signal(s) to the first child, want none", n)
	}
	if !childrec.ReadsAliveForTest(first) {
		t.Errorf("the first child %d is not alive after the kill of the id", first)
	}
}

// TestChildEndRemovesRecordBeforeDrain pins when the record goes: at the end of the
// child, not at the exit frame. The child leaves a grandchild on its stdout, so the
// exit frame waits for the whole drain grace (89cb6289, measured on Linux: the record
// went 60 ms after the end of the child and the frame came 5 s later). It also pins
// the pipe-drain line and its place before the read-error line.
func TestChildEndRemovesRecordBeforeDrain(t *testing.T) {
	old := supervisor.SetExitDrainGraceForTest(4 * time.Second)
	t.Cleanup(func() { supervisor.SetExitDrainGraceForTest(old) })
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
		Result rpc.SpawnResult `json:"result"`
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
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
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
		t.Error("the record was still there 2 s after the end of the child, inside a 4 s drain")
	}
	if exited {
		t.Error("the exit frame came before the check, so the row does not show the order")
	}
	// The exit frame comes at the end of the 4 s drain. This loop waits longer than
	// waitExit does, for a loaded host.
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
	// The read-error line is searched only after the drain line, so a late line
	// of another test that also used the id c1 cannot match before it.
	readErr := -1
	if drain >= 0 {
		readErr = strings.Index(out[drain:], "[process.Manager] stdout read error for process c1")
	}
	if drain < 0 || readErr < 0 {
		t.Errorf("want the pipe-drain line and then the stdout read-error line, got:\n%s", out)
	}
}

// TestShutdownRemovesChildRecords pins that a graceful shutdown leaves no record: the
// daemon waits for the children it killed, and the end of each one removes its record
// (89cb6289, measured on Linux: children/ is empty after a -stop).
func TestShutdownRemovesChildRecords(t *testing.T) {
	// The wait ends as soon as both children are reaped. A wide bound keeps a slow
	// runner from ending it first.
	old := childrec.SetShutdownRecordWaitForTest(30 * time.Second)
	t.Cleanup(func() { childrec.SetShutdownRecordWaitForTest(old) })
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

// TestKeepChildrenSkipsShutdownWait pins that a shutdown with -keep-children does not
// wait for the end of the children. The children stay alive, so a wait for them takes
// the whole bound. The bound is 30 s here, and the call returns at once.
//
// It also pins claustrum's own rule for the records: the kept children lose their
// records at that shutdown, so the reap of the next start sends them no signal. The
// records go before the run-dir lock is released, so a successor that takes the lock
// finds none of them. The reap below reads the real processes, and only its signal
// is seamed.
func TestKeepChildrenSkipsShutdownWait(t *testing.T) {
	old := childrec.SetShutdownRecordWaitForTest(30 * time.Second)
	t.Cleanup(func() { childrec.SetShutdownRecordWaitForTest(old) })
	s, cl, runDir := runShapedServer(t)
	s.keepChildren = true
	t.Cleanup(s.procs.KillAll) // the kept children must end with the test
	pids := []int{spawnSleeper(t, cl, "c1"), spawnSleeper(t, cl, "c2")}
	if n := len(childrenNames(t, filepath.Join(runDir, "children"))); n != 2 {
		t.Fatalf("%d records while the children run, want 2", n)
	}
	// The seam runs on the closeAll goroutine, so it records and does not fail the test.
	var atRelease []os.DirEntry
	var releaseErr error
	released := false
	s.releaseRunDir = func() {
		atRelease, releaseErr = os.ReadDir(filepath.Join(runDir, "children"))
		released = true
	}

	done := make(chan struct{})
	go func() { defer close(done); s.closeAll(filepath.Join(runDir, "rpc.sock")) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		// End the test's own children, so the wait and the test end.
		s.procs.KillAll()
		<-done
		t.Fatal("the shutdown with -keep-children waited for the records")
	}
	if !released {
		t.Fatal("closeAll did not run the run-dir release")
	}
	if releaseErr != nil || len(atRelease) != 0 {
		t.Errorf("records at the run-dir release: %d (read error %v), want none for the kept children", len(atRelease), releaseErr)
	}
	if names := childrenNames(t, filepath.Join(runDir, "children")); len(names) != 0 {
		t.Errorf("records after the shutdown step: %v, want none for the kept children", names)
	}
	for _, pid := range pids {
		if err := syscall.Kill(pid, 0); err != nil {
			t.Errorf("kept child %d is gone: %v", pid, err)
		}
	}

	// The next daemon on this socket: its reap must send nothing. The signal is
	// seamed, so a reap that finds a record ends no child of this test.
	oldKill := procinfo.KillGroupForTest()
	var oldSleep func(time.Duration)
	t.Cleanup(func() { procinfo.SetKillGroupForTest(oldKill); childrec.SetReapSleepForTest(oldSleep) })
	var kills []childrec.GroupSignalForTest
	procinfo.SetKillGroupForTest(func(pid int, sig syscall.Signal) error {
		kills = append(kills, childrec.GroupSignalForTest{Pid: pid, Sig: sig})
		return nil
	})
	oldSleep = childrec.SetReapSleepForTest(func(time.Duration) {})
	(&childrec.Records{RunDir: runDir, InstanceID: "inst-next"}).ReapOrphans()
	if len(kills) != 0 {
		t.Errorf("the reap of the next start signalled kept children: %v", kills)
	}
}

// TestChildrenNotADirectory pins a children entry that is not a real folder: no record
// is written, nothing is written through a symlink, and the two log lines come in
// order (`89cb6289`, measured on Linux). The removal at the end of the child is still
// tried, and its failure is logged before the exit line.
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
// child (`89cb6289`, measured on Linux).
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
// the child still removes the record (`89cb6289`, measured on Linux).
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
