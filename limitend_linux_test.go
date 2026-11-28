package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// limitLineServer boots a line server with the limit of 15 minutes that the host
// settings file sets in the rows.
func limitLineServer(t *testing.T) (*server, string) {
	t.Helper()
	return limitLineServerKeep(t, false)
}

func limitLineServerKeep(t *testing.T, keepChildren bool) (*server, string) {
	t.Helper()
	// The dials of the test come from the own pid, so they count as a client here.
	old := limitSelfPID.Swap(-1)
	t.Cleanup(func() { limitSelfPID.Store(old) })
	s, sock := bootLineServer(t, keepChildren)
	s.hostLimit = hostLimit{path: hostLimitPath, minutes: 15}
	return s, sock
}

// TestLimitEndClockSkipsASelfDial: a connection that this daemon dials to itself, such
// as the orphan self-probe, does not start the clock again, also with the token
// (claustrum's own, not measured).
func TestLimitEndClockSkipsASelfDial(t *testing.T) {
	s, sock := bootLineServer(t, false)
	s.hostLimit = hostLimit{path: hostLimitPath, minutes: 15}
	old := int64(monoNow() - time.Hour)
	s.lastAttended.Store(old)
	cl := dial(t, sock)
	if reply := string(cl.call(authed(`{"jsonrpc":"2.0","id":1,"method":"server.ping"}`))); !strings.Contains(reply, `"result"`) {
		t.Fatalf("server.ping = %s", reply)
	}
	_ = cl.nc.Close()
	waitConnsExactly(t, s, 0)
	if got := s.lastAttended.Load(); got != old {
		t.Errorf("a dial of the daemon to itself started the clock again: %d, want %d", got, old)
	}
}

// spawnMarkedSleeper spawns one helper sleeper with both child marks, on its own
// connection, and returns that connection. A sweep before the group kills ends it, so a
// test with it pins the order of the two steps.
func spawnMarkedSleeper(t *testing.T, sock, id string) *testClient {
	t.Helper()
	exe, env := helperCommand(t, "sleep")
	env[daemonChildMarker] = "1"
	params, err := json.Marshal(map[string]any{"id": id, "command": exe, "args": []string{"60"}, "env": env})
	if err != nil {
		t.Fatal(err)
	}
	cl := dial(t, sock)
	if reply := string(cl.call(authed(`{"jsonrpc":"2.0","id":1,"method":"process.spawn","params":` + string(params) + `}`))); !strings.Contains(reply, `"success":true`) {
		t.Fatalf("spawn %s = %s", id, reply)
	}
	return cl
}

// TestLimitEndLines: a client spawns two children and leaves. 15 minutes after the close
// the check ends the daemon with the line of 13047a51, which counts the live children
// (measured on Linux). The teardown then ends both groups and removes the socket. The
// children have both marks, so "ended 0 more" shows that the sweep comes after the kills.
func TestLimitEndLines(t *testing.T) {
	s, sock := limitLineServer(t)
	c1 := spawnMarkedSleeper(t, sock, "c1")
	c2 := spawnMarkedSleeper(t, sock, "c2")
	_ = c1.nc.Close()
	_ = c2.nc.Close()
	waitConnsExactly(t, s, 0)
	buf := captureLogBuf(t)
	left := time.Duration(s.lastAttended.Load())
	var w selfExitWatch
	if s.selfExitCheck(sock, &w, left+15*time.Minute-time.Second) {
		t.Fatalf("the daemon ended before the limit:\n%s", buf.String())
	}
	if !s.selfExitCheck(sock, &w, left+15*time.Minute+50*time.Second+600*time.Millisecond) {
		t.Fatalf("no end at the limit:\n%s", buf.String())
	}
	if !s.limitStopped.Load() {
		t.Error("the end at the limit did not mark the server")
	}
	s.closeAll(sock)
	wantLinesInOrder(t, buf.String(),
		"[Server] nobody has used this helper or been connected to it for 15m50s, and the unattended limit in force is 15 minutes (/etc/claude-desktop-ssh/settings.json): shutting down and killing 2 child process(es)",
		"[Server] shutdown requested",
		"[Server] cleanup: ended 0 more process(es) that the children had started and left",
		"[Server] cleanup: closed 0 connection(s), ended 2 child process group(s)")
	for _, id := range []string{"c1", "c2"} {
		select {
		case <-s.procs.Get(id).DoneForTest():
		case <-time.After(10 * time.Second):
			t.Errorf("child %s did not end after the end at the limit", id)
		}
	}
	if _, err := os.Lstat(sock); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the socket stayed after the end at the limit: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(sock), "daemon.token")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("daemon.token stayed after the end at the limit: %v", err)
	}
}

// TestLimitEndWaitsForAClient: a connected client holds the end off (claustrum's own, not
// measured).
func TestLimitEndWaitsForAClient(t *testing.T) {
	s, sock := limitLineServer(t)
	dial(t, sock)
	waitConnsExactly(t, s, 1)
	buf := captureLogBuf(t)
	var w selfExitWatch
	if s.selfExitCheck(sock, &w, time.Duration(s.lastAttended.Load())+time.Hour) {
		t.Fatal("the daemon ended at the limit while a client was connected")
	}
	if strings.Contains(buf.String(), "nobody has used this helper") {
		t.Errorf("a limit line came while a client was connected:\n%s", buf.String())
	}
}

// TestLimitEndClockRestartsAtClose: the close of a connection starts the clock again
// (claustrum's own, not measured). The rows end about 15 minutes after the last close
// (measured on Linux).
func TestLimitEndClockRestartsAtClose(t *testing.T) {
	s, sock := limitLineServer(t)
	s.lastAttended.Store(int64(monoNow() - time.Hour))
	cl := dial(t, sock)
	if reply := string(cl.call(authed(`{"jsonrpc":"2.0","id":1,"method":"server.ping"}`))); !strings.Contains(reply, `"result"`) {
		t.Fatalf("server.ping = %s", reply)
	}
	_ = cl.nc.Close()
	waitConnsExactly(t, s, 0)
	var w selfExitWatch
	if s.selfExitCheck(sock, &w, monoNow()+14*time.Minute) {
		t.Fatal("the daemon ended 14 minutes after a close: the close did not restart the clock")
	}
}

// TestLimitEndClockSkipsUnusedConnections: a dial that sends nothing, as the host
// cleaner makes, and a request with no token do not start the clock again (claustrum's
// own, not measured).
func TestLimitEndClockSkipsUnusedConnections(t *testing.T) {
	s, sock := limitLineServer(t)
	old := int64(monoNow() - time.Hour)
	s.lastAttended.Store(old)
	silent := dial(t, sock)
	waitConnsExactly(t, s, 1)
	_ = silent.nc.Close()
	waitConnsExactly(t, s, 0)
	tokenless := dial(t, sock)
	if reply := string(tokenless.call(`{"jsonrpc":"2.0","id":1,"method":"server.ping"}`)); !strings.Contains(reply, `"error"`) {
		t.Fatalf("a server.ping with no token = %s", reply)
	}
	_ = tokenless.nc.Close()
	waitConnsExactly(t, s, 0)
	if got := s.lastAttended.Load(); got != old {
		t.Errorf("a connection that showed no token started the clock again: %d, want %d", got, old)
	}
}

// TestLimitEndNeedsALimit: a daemon with no limit from the host settings file never ends
// at a limit.
func TestLimitEndNeedsALimit(t *testing.T) {
	s, sock := bootLineServer(t, false)
	buf := captureLogBuf(t)
	var w selfExitWatch
	if s.selfExitCheck(sock, &w, time.Duration(s.lastAttended.Load())+100*time.Hour) {
		t.Fatalf("a daemon with no limit ended:\n%s", buf.String())
	}
}

// leaveOneProcess spawns a child that starts a sleeper in a new session and exits. It
// returns the pid of the sleeper, which keeps the marks of the child. The cleanup ends
// that sleeper, a grandchild of this test, if it still runs.
func leaveOneProcess(t *testing.T, s *server, sock string) int {
	t.Helper()
	exe, env := helperCommand(t, "leave-own-session")
	// A daemon gets this mark at its re-exec and passes it on. The test process has no
	// re-exec, so the spawn sets it.
	env[daemonChildMarker] = "1"
	pidFile := filepath.Join(t.TempDir(), "left.pid")
	params, err := json.Marshal(map[string]any{"id": "l1", "command": exe, "args": []string{pidFile}, "env": env})
	if err != nil {
		t.Fatal(err)
	}
	cl := dial(t, sock)
	if reply := string(cl.call(authed(`{"jsonrpc":"2.0","id":1,"method":"process.spawn","params":` + string(params) + `}`))); !strings.Contains(reply, `"success":true`) {
		t.Fatalf("spawn l1 = %s", reply)
	}
	select {
	case <-s.procs.Get("l1").DoneForTest():
	case <-time.After(10 * time.Second):
		t.Fatal("the child l1 did not exit within 10 s")
	}
	_ = cl.nc.Close()
	waitConnsExactly(t, s, 0)
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(b))
	if err != nil {
		t.Fatal(err)
	}
	// The cleanup kills through a pid file descriptor that names this sleeper, so a
	// reused number takes no signal.
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		t.Fatalf("pidfd_open %d: %v", pid, err)
	}
	t.Cleanup(func() {
		_ = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
		_ = unix.Close(fd)
	})
	return pid
}

// processLives is true while pid names a process that is not a zombie.
func processLives(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	i := strings.LastIndexByte(string(b), ')')
	return i < 0 || i+2 >= len(b) || b[i+2] != 'Z'
}

// TestLimitEndSweepsALeftProcess: after the end at the limit, a process that a child
// started in its own session and left ends, and the sweep line counts it before the
// cleanup line (13047a51, measured on Linux).
func TestLimitEndSweepsALeftProcess(t *testing.T) {
	s, sock := limitLineServer(t)
	pid := leaveOneProcess(t, s, sock)
	buf := captureLogBuf(t)
	var w selfExitWatch
	if !s.selfExitCheck(sock, &w, time.Duration(s.lastAttended.Load())+16*time.Minute) {
		t.Fatal("no end at the limit")
	}
	s.closeAll(sock)
	if strings.Contains(buf.String(), "can't be shown to lead") {
		t.Errorf("the socket line came for a socket path that leads here:\n%s", buf.String())
	}
	wantLinesInOrder(t, buf.String(),
		"[Server] shutdown requested",
		"[Server] cleanup: ended 1 more process(es) that the children had started and left",
		"[Server] cleanup: closed 0 connection(s), ended 0 child process group(s)")
	deadline := time.Now().Add(5 * time.Second)
	for processLives(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("the left process %d still runs after the sweep", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestLimitEndSocketGoneSkipsTheSweep: at the end at the limit a socket path that is
// gone or holds a regular file gives the line of 13047a51, and no sweep runs. The left
// process stays alive (measured on Linux).
func TestLimitEndSocketGoneSkipsTheSweep(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replace bool
	}{{"gone", false}, {"regular_file", true}} {
		t.Run(tc.name, func(t *testing.T) {
			s, sock := limitLineServer(t)
			pid := leaveOneProcess(t, s, sock)
			if err := os.Remove(sock); err != nil {
				t.Fatal(err)
			}
			if tc.replace {
				if err := os.WriteFile(sock, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			buf := captureLogBuf(t)
			var w selfExitWatch
			if !s.selfExitCheck(sock, &w, time.Duration(s.lastAttended.Load())+16*time.Minute) {
				t.Fatal("no end at the limit")
			}
			s.closeAll(sock)
			wantLinesInOrder(t, buf.String(),
				"[Server] socket path "+sock+" can't be shown to lead to this helper any more, so only its children's own process groups are ended: what has left one may still be running",
				"[Server] shutdown requested")
			if strings.Contains(buf.String(), "that the children had started and left") {
				t.Errorf("a sweep ran for a socket path that no longer leads here:\n%s", buf.String())
			}
			if !processLives(pid) {
				t.Errorf("the left process %d ended, but the socket path no longer leads here", pid)
			}
		})
	}
}

// TestLimitEndKeepChildrenSkipsTheSweep: with -keep-children no sweep runs, and the
// left process stays (claustrum's own, not measured).
func TestLimitEndKeepChildrenSkipsTheSweep(t *testing.T) {
	s, sock := limitLineServerKeep(t, true)
	pid := leaveOneProcess(t, s, sock)
	buf := captureLogBuf(t)
	var w selfExitWatch
	if !s.selfExitCheck(sock, &w, time.Duration(s.lastAttended.Load())+16*time.Minute) {
		t.Fatal("no end at the limit")
	}
	s.closeAll(sock)
	if strings.Contains(buf.String(), "that the children had started and left") {
		t.Errorf("a sweep ran with -keep-children:\n%s", buf.String())
	}
	if !processLives(pid) {
		t.Errorf("the left process %d ended with -keep-children", pid)
	}
}

// TestShutdownRunsNoSweep: a plain shutdown runs no sweep, as 13047a51 logs no sweep
// line after server.shutdown (measured on Linux).
func TestShutdownRunsNoSweep(t *testing.T) {
	s, sock := limitLineServer(t)
	pid := leaveOneProcess(t, s, sock)
	buf := captureLogBuf(t)
	s.requestShutdown()
	s.closeAll(sock)
	if strings.Contains(buf.String(), "that the children had started and left") {
		t.Errorf("a plain shutdown ran the sweep:\n%s", buf.String())
	}
	if !processLives(pid) {
		t.Errorf("the left process %d ended at a plain shutdown", pid)
	}
}

// TestLimitEndAfterAnotherShutdown: once another path started the shutdown, a due check
// logs no limit line and sets no mark, so no sweep runs (claustrum's own, not measured).
func TestLimitEndAfterAnotherShutdown(t *testing.T) {
	s, sock := limitLineServer(t)
	buf := captureLogBuf(t)
	s.requestShutdown()
	var w selfExitWatch
	s.selfExitCheck(sock, &w, time.Duration(s.lastAttended.Load())+time.Hour)
	if strings.Contains(buf.String(), "nobody has used this helper") || s.limitStopped.Load() {
		t.Errorf("the limit end ran after another shutdown began:\n%s", buf.String())
	}
}

// TestLimitEndLosesTheRace: another path takes the one start of the shutdown after the
// begun test of limitEnd. Then limitEnd logs no line and sets no mark.
func TestLimitEndLosesTheRace(t *testing.T) {
	s, sock := limitLineServer(t)
	buf := captureLogBuf(t)
	s.once.Do(func() {}) // the start is taken, and the shutdown channel is still open
	if s.limitEnd(sock, 0, time.Duration(s.lastAttended.Load())+time.Hour) {
		t.Error("limitEnd reported a start that another path took")
	}
	if strings.Contains(buf.String(), "nobody has used this helper") || strings.Contains(buf.String(), "shutdown requested") || s.limitStopped.Load() {
		t.Errorf("limitEnd logged or marked after it lost the start:\n%s", buf.String())
	}
	close(s.shutdown) // the taken start never closes it, so the test does
}
