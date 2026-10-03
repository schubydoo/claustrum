//go:build linux || darwin

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// otherShapeServer boots the harness daemon on <base>/b/s.sock, a socket that is not
// run/<id>/rpc.sock, and returns a client and the folder of the socket.
func otherShapeServer(t *testing.T) (*testClient, string) {
	t.Helper()
	base, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	dir := filepath.Join(base, "b")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, sock := newRunningServerAt(t, filepath.Join(dir, "s.sock"))
	return dial(t, sock), dir
}

// spawnEnviron spawns the environ helper with the caller env and returns its pid and
// its environment lines.
func spawnEnviron(t *testing.T, cl *testClient, id string, env map[string]string) (int, []string) {
	t.Helper()
	exe, helperEnv := helperCommand(t, "environ")
	for k, v := range helperEnv {
		env[k] = v
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "process.spawn", "auth": testToken,
		"params": map[string]any{"id": id, "command": exe, "env": env, "wantPid": true},
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
	out := streamBytes(t, cl.waitExit(id), "stdout")
	return reply.Result.Pid, strings.Split(strings.TrimSuffix(out, "\n"), "\n")
}

// unsetEnv removes the variables from the environment of this process until the test
// ends. A variable that a test sets after it is then a new last entry.
func unsetEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		old, had := os.LookupEnv(k)
		os.Unsetenv(k)
		t.Cleanup(func() {
			if had {
				os.Setenv(k, old)
			} else {
				os.Unsetenv(k)
			}
		})
	}
}

// TestChildEnvOnAnotherSocketShape pins the child environment of a daemon whose socket
// is not run/<id>/rpc.sock (89cb6289, Linux and macOS rows F2, F5 and K3, with the
// caller keys of rows EV02a and EV03a). The environment ends with CLAUDE_SSH_RUN_DIR
// and then CLAUDE_SSH_CHILD. A caller key that the daemon environment holds keeps the
// place of that entry. A new caller key comes after the daemon entries. A caller value
// and a daemon value of the two entries do not reach the child.
func TestChildEnvOnAnotherSocketShape(t *testing.T) {
	// The daemon environment of the rows: EFX, then copies of the two entries, then
	// CLAUDE_SSH_DAEMON_CHILD as the last entry.
	unsetEnv(t, "EFX", envChildMarker, envRunDir, daemonChildMarker, "ZZNEW")
	os.Setenv("EFX", "old")
	os.Setenv(envChildMarker, "x")
	os.Setenv(envRunDir, "/y")
	os.Setenv(daemonChildMarker, "1")
	cl, dir := otherShapeServer(t)

	for i := range 5 { // row F5: the order is the same in every spawn
		pid, got := spawnEnviron(t, cl, "e"+strconv.Itoa(i), map[string]string{
			"ZZNEW": "1", "EFX": "q", daemonChildMarker: "", envChildMarker: "caller", envRunDir: "/caller",
		})
		if len(got) < 5 {
			t.Fatalf("child environment has %d lines: %q", len(got), got)
		}
		// The key of the test helper is a new caller key too. The two new keys have no
		// fixed order between them (rows P9 and N7).
		tail := got[len(got)-5:]
		start := regexp.MustCompile(`^` + envChildMarker + `=` + strconv.Itoa(pid) + `:\S.*$`)
		newKeys := slices.Sorted(slices.Values(tail[1:3]))
		if tail[0] != daemonChildMarker+"=" || newKeys[0] != "CLAUSTRUM_TEST_HELPER=environ" || newKeys[1] != "ZZNEW=1" ||
			tail[3] != envRunDir+"="+dir || !start.MatchString(tail[4]) {
			t.Fatalf("spawn %d: the environment ends %q\nwant %s=, the helper key, ZZNEW=1, %s=%s, %s=%d:<start>",
				i, tail, daemonChildMarker, envRunDir, dir, envChildMarker, pid)
		}
		for _, name := range []string{envRunDir, envChildMarker, "EFX"} {
			n := 0
			for _, e := range got {
				if strings.HasPrefix(e, name+"=") {
					n++
				}
			}
			if n != 1 {
				t.Errorf("spawn %d: %d entries of %s, want 1: %q", i, n, name, got)
			}
		}
		// EFX keeps its place in the daemon environment, before the daemon's
		// CLAUDE_SSH_DAEMON_CHILD. A caller key that is added, not replaced in place,
		// comes after that entry.
		if at := slices.Index(got, "EFX=q"); at < 0 || at >= len(got)-5 {
			t.Errorf("spawn %d: EFX=q is at place %d of %d, want it before %s: %q", i, at, len(got), daemonChildMarker, got)
		}
	}
}

// TestChildEnvKeepsThePlaceOfAGoRuntimeVariable pins Linux rows P4a to P4c (89cb6289).
// A GOGC of the daemon environment keeps its place there. A GOMAXPROCS of the spawn env
// stands where a new caller key stands. CLAUDE_SSH_RUN_DIR and CLAUDE_SSH_CHILD are the
// last two entries.
func TestChildEnvKeepsThePlaceOfAGoRuntimeVariable(t *testing.T) {
	unsetEnv(t, "EFX", "GOGC", "GOMAXPROCS", envChildMarker, envRunDir, daemonChildMarker)
	os.Setenv("EFX", "old")
	os.Setenv("GOGC", "50")
	os.Setenv(daemonChildMarker, "1")
	cl, dir := otherShapeServer(t)

	pid, got := spawnEnviron(t, cl, "e1", map[string]string{"GOMAXPROCS": "2"})
	// The key of the test helper is a new caller key too. The two new keys have no
	// fixed order between them (rows P9 and N7), so the test puts them in name order.
	want := []string{"EFX=old", "GOGC=50", daemonChildMarker + "=1", "CLAUSTRUM_TEST_HELPER=environ",
		"GOMAXPROCS=2", envRunDir + "=" + dir}
	if len(got) < len(want)+1 {
		t.Fatalf("child environment has %d lines: %q", len(got), got)
	}
	tail := slices.Clone(got[len(got)-len(want)-1:])
	slices.Sort(tail[3:5])
	if !slices.Equal(tail[:len(want)], want) {
		t.Errorf("the environment ends %q\nwant %q and then %s", tail, want, envChildMarker)
	}
	if last := tail[len(want)]; !strings.HasPrefix(last, envChildMarker+"="+strconv.Itoa(pid)+":") {
		t.Errorf("the last entry is %q, want %s=%d:<start>", last, envChildMarker, pid)
	}
}

// TestChildRecordOnAnotherSocketShape pins that the record of a child goes to
// <folder of the socket>/children on a socket that is not run/<id>/rpc.sock (89cb6289,
// Linux and macOS rows F2 and K3).
func TestChildRecordOnAnotherSocketShape(t *testing.T) {
	cl, dir := otherShapeServer(t)
	pid := spawnSleeper(t, cl, "c1")
	rec := filepath.Join(dir, "children", strconv.Itoa(pid)+".json")
	fi, err := os.Stat(rec)
	if err != nil {
		t.Fatalf("no record on this socket shape: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("record mode %v, want 0600", fi.Mode().Perm())
	}
	killChild(t, cl, "c1")
}

// TestChildRecordOnARelativeSocket pins row F3: a relative socket path s.sock gives
// CLAUDE_SSH_RUN_DIR=. and the record goes to children/ in the working folder.
func TestChildRecordOnARelativeSocket(t *testing.T) {
	base, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	t.Chdir(base)
	_, sock := newRunningServerAt(t, "s.sock")
	cl := dial(t, sock)

	_, env := spawnEnviron(t, cl, "e1", map[string]string{})
	if len(env) < 2 || env[len(env)-2] != envRunDir+"=." {
		t.Errorf("the environment ends %q, want %s=. before the last entry", env[max(0, len(env)-2):], envRunDir)
	}
	pid := spawnSleeper(t, cl, "c1")
	if _, err := os.Stat(filepath.Join(base, "children", strconv.Itoa(pid)+".json")); err != nil {
		t.Errorf("no record in the working folder: %v", err)
	}
	killChild(t, cl, "c1")
}

// TestReapOrphansOnAnotherSocketShape pins that the reap of a start runs on a socket
// that is not run/<id>/rpc.sock (89cb6289, Linux and macOS rows EV01a to EV01i). The
// run dir is the folder that execChildRunDir gives for such a socket.
func TestReapOrphansOnAnotherSocketShape(t *testing.T) {
	if node, _ := ownReapIdentity(); node == "" {
		t.Skip("no node identity on this host")
	}
	runDir := execChildRunDir(filepath.Join(t.TempDir(), "s.sock"))
	if runDir == "" {
		t.Fatal("no run dir for a socket of another shape")
	}
	const pid = 800000101
	writeRec(t, runDir, orphanRecord(pid, "/f/bin/sigstub", ""))
	sim := newProcSim(t)
	sim.live[pid] = &liveProc{state: procAlive, startTicks: "9000", pgid: pid,
		program: "/f/bin/sigstub", runDir: runDir, childMark: strconv.Itoa(pid) + ":9000"}
	sim.diesOn(pid, syscall.SIGTERM)

	reapOrphans(runDir, "inst-self")

	if sim.sigCount(syscall.SIGTERM) != 1 || !sim.signaled(pid) {
		t.Errorf("kills = %v, want one SIGTERM to group %d", sim.kills, pid)
	}
}
