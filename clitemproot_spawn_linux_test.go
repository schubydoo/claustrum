package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/supervisor"
)

func spawnLine(t *testing.T, id, command string, env map[string]string, flag bool) string {
	t.Helper()
	params := map[string]any{"id": id, "command": command, "env": env}
	if flag {
		params["cliTempRoot"] = true
	}
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "process.spawn", "auth": testToken, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func tempRootsLive() int {
	tempRoots.mu.Lock()
	defer tempRoots.mu.Unlock()
	return tempRoots.live
}

// waitTempRootsLive polls the live count until it is n, with a limit.
func waitTempRootsLive(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for tempRootsLive() != n {
		if time.Now().After(deadline) {
			t.Fatalf("live count %d, want %d", tempRootsLive(), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The frame of the spawn is the measured one, the child gets the folder, and the
// exit of the child gives the folder back.
func TestSpawnCLITempRootFrame(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	old := newTempRootName
	newTempRootName = func() string { return "ccd-a5247ffbe0" }
	t.Cleanup(func() { newTempRootName = old })
	x, _ := tempRootDir(t, true)
	_, sock := newRunningServer(t)
	cl := dial(t, sock)
	exe, env := helperCommand(t, "environ")
	env["TMPDIR"] = x
	raw := cl.call(spawnLine(t, "f1b", exe, env, true))
	want := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"cliTempRoot":{"refused":"not readable by its owner","name":"ccd-a5247ffbe0"}}}`
	if string(raw) != want {
		t.Fatalf("reply %s, want %s", raw, want)
	}
	lines := strings.Split(streamBytes(t, cl.waitExit("f1b"), "stdout"), "\n")
	if !slices.Contains(lines, "CLAUDE_CODE_TMPDIR="+filepath.Join(x, "ccd-a5247ffbe0", "t")) || !slices.Contains(lines, "TMPDIR="+x) {
		t.Errorf("child env %q, want the folder and TMPDIR", lines)
	}
	waitTempRootsLive(t, 0)
}

// Without the flag the reply and the env stay as before.
func TestSpawnWithoutCLITempRootAddsNothing(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	x, _ := tempRootDir(t, true)
	_, sock := newRunningServer(t)
	cl := dial(t, sock)
	exe, env := helperCommand(t, "environ")
	env["TMPDIR"] = x
	if raw := cl.call(spawnLine(t, "p", exe, env, false)); string(raw) != `{"jsonrpc":"2.0","id":1,"result":{"success":true}}` {
		t.Fatalf("reply %s", raw)
	}
	out := streamBytes(t, cl.waitExit("p"), "stdout")
	if strings.Contains(out, "CLAUDE_CODE_TMPDIR=") || len(ccdFolders(t, x)) != 0 {
		t.Errorf("a folder without the flag: %q", out)
	}
}

// A spawn that does not start gives the folder back.
func TestSpawnCLITempRootStartFailureGivesItBack(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	x, _ := tempRootDir(t, true)
	_, sock := newRunningServer(t)
	cl := dial(t, sock)
	raw := cl.call(spawnLine(t, "gone", filepath.Join(x, "no-such-program"), map[string]string{"TMPDIR": x}, true))
	if !strings.Contains(string(raw), `"error"`) {
		t.Fatalf("reply %s, want an error", raw)
	}
	waitTempRootsLive(t, 0)
	// The step ran before the start, and the folder that it made stays.
	if got := ccdFolders(t, x); len(got) != 1 {
		t.Errorf("folders %v, want one", got)
	}
}

// A launched spawn skips the step: the reply has no member, no folder is made and
// the child gets no CLAUDE_CODE_TMPDIR.
func TestSpawnCLITempRootSkipsALauncher(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	x, _ := tempRootDir(t, true)
	wrap, exe := wrapLauncherFixture(t)
	_, sock := newRunningServer(t)
	cl := dial(t, sock)
	env := map[string]string{"CLAUSTRUM_TEST_HELPER": "wrap", "CLAUSTRUM_TEST_WRAP_NEXT": "environ", "TMPDIR": x}
	raw := cl.call(launchedSpawnReq(t, map[string]any{"id": "l1", "command": exe, "env": env, "launcher": []string{wrap}, "cliTempRoot": true}))
	if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`; string(raw) != want {
		t.Fatalf("reply %s, want %s", raw, want)
	}
	out := streamBytes(t, cl.waitExit("l1"), "stdout")
	if !strings.Contains(out, "TMPDIR="+x+"\n") || strings.Contains(out, "CLAUDE_CODE_TMPDIR=") || len(ccdFolders(t, x)) != 0 {
		t.Errorf("child env %q, folders %v, want TMPDIR alone and no folder", out, ccdFolders(t, x))
	}
}

// A step or a refresh that holds tempRoots.mu across a stuck file call does not hold
// the exit of a child with a folder. killAndWait answers died:true at the exit and
// sends no group SIGKILL (b3081b51, measured on Linux). The test holds the lock in
// place of the stuck call. The folder goes back once the lock is free.
func TestSpawnCLITempRootExitDoesNotWaitForTheLock(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	x, _ := tempRootDir(t, true)
	s, sock := newRunningServer(t)
	cl := dial(t, sock)
	exe, env := helperCommand(t, "cat")
	env["TMPDIR"] = x
	raw := cl.call(spawnLine(t, "kw", exe, env, true))
	if !strings.Contains(string(raw), `"name":"ccd-`) {
		t.Fatalf("spawn reply %s, want a folder", raw)
	}
	// The stub counts each group SIGKILL to the child and delivers none.
	p := s.procs.Get("kw")
	realSignal := supervisor.SignalGroupForTest()
	var kills atomic.Int32
	supervisor.SetSignalGroupForTest(func(g *supervisor.ProcGroup, proc *os.Process, signame string) error {
		if proc == p.CmdForTest().Process && signame == "KILL" {
			kills.Add(1)
			return nil
		}
		return realSignal(g, proc, signame)
	})
	t.Cleanup(func() { supervisor.SetSignalGroupForTest(realSignal) })
	// A short reap bound makes a wait that misses the exit answer died:false at once.
	oldReap := supervisor.SetKillReapGraceForTest(100 * time.Millisecond)
	t.Cleanup(func() { supervisor.SetKillReapGraceForTest(oldReap) })
	var unlock sync.Once
	tempRoots.mu.Lock()
	free := func() { unlock.Do(tempRoots.mu.Unlock) }
	t.Cleanup(free)
	start := time.Now()
	got := string(cl.call(authed(`{"jsonrpc":"2.0","id":2,"method":"process.killAndWait","params":{"id":"kw","signal":"SIGTERM","timeoutMs":3000,"escalate":true}}`)))
	took := time.Since(start)
	if want := replyLine(2, `{"found":true,"died":true}`); got != want {
		t.Errorf("killAndWait reply %s after %v, want %s", got, took, want)
	}
	if took > 2*time.Second {
		t.Errorf("killAndWait took %v, want an answer at the exit", took)
	}
	if n := kills.Load(); n != 0 {
		t.Errorf("group SIGKILLs to the child: %d, want 0", n)
	}
	if n := tempRoots.live; n != 1 { // the test holds tempRoots.mu
		t.Errorf("live count %d while the lock is held, want 1", n)
	}
	free()
	waitTempRootsLive(t, 0)
}

// A spawn with cliTempRoot and cliTempRootEnd is ended when its folder is lost, and
// its exit frame carries the killedBy "temp_root" (4c111e86, measured on Linux and
// macOS). The child is the helper, which ends on the SIGTERM.
func TestSpawnCLITempRootEndOnALoss(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	setTempRootCheckEvery(t, 20*time.Millisecond)
	x, _ := tempRootDir(t, true)
	_, sock := newRunningServer(t)
	cl := dial(t, sock)
	exe, env := helperCommand(t, "cat")
	env["TMPDIR"] = x
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "process.spawn", "auth": testToken,
		"params": map[string]any{"id": "te", "command": exe, "env": env, "cliTempRoot": true, "cliTempRootEnd": true}})
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Result struct {
			CLITempRoot struct{ Name string } `json:"cliTempRoot"`
		} `json:"result"`
	}
	if err := json.Unmarshal(cl.call(string(b)), &reply); err != nil || reply.Result.CLITempRoot.Name == "" {
		t.Fatalf("spawn reply %+v %v, want a folder", reply, err)
	}
	if err := os.Chmod(filepath.Join(x, reply.Result.CLITempRoot.Name), 0o777); err != nil {
		t.Fatal(err)
	}
	frames := cl.waitExit("te")
	exit := frames[len(frames)-1]
	if exit.Stream != "exit" || exit.KilledBy != "temp_root" || exit.Signal != "SIGTERM" {
		t.Errorf("exit frame %+v, want SIGTERM and killedBy temp_root", exit)
	}
	waitTempRootsLive(t, 0)
}
