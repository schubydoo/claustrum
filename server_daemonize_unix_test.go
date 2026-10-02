//go:build unix

package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestServeDaemonizeWithAmbientChildMarker is the end-to-end regression guard for
// the CLAUDE_SSH_DAEMON_CHILD env-var collision (diagnosed 2026-06-12). A
// claustrum launched from inside a real claude-ssh session inherits
// CLAUDE_SSH_DAEMON_CHILD=1 ambiently; before the fix that made
// `claustrum -serve -token-fd 0` boot straight into its detached-child branch,
// skip the parent token-forward/daemonize path, fall through to an empty
// -token-file, and die with `read --token-file: open :`. The fix keys the
// re-exec sentinel off the claustrum-namespaced CLAUSTRUM_DAEMON_CHILD instead.
//
// The in-process harness (newRunningServer) never re-execs, so it cannot catch
// this. Here we build the real binary and run it exactly as clauster does — token
// on stdin via -token-fd 0 — but with the ambient marker set, asserting the
// daemon daemonizes, opens the socket, and authenticates the forwarded token.
func TestServeDaemonizeWithAmbientChildMarker(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the real binary; skipped under -short")
	}

	bin := buildClaustrum(t)

	// Short socket dir (/tmp/…) keeps the AF_UNIX path under the macOS sun_path
	// limit, unlike t.TempDir()'s long test-name-embedding path.
	dir, err := os.MkdirTemp("", "cld")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	const token = "ambient-collision-token"

	// Redirect the launcher's stdio to a file rather than a pipe. The child no
	// longer inherits these — it writes to remote-server.log beside the socket
	// (see TestServeWritesRemoteServerLog) — but a launcher that ever did hold
	// them open for its whole life would make CombinedOutput's Wait block on EOF
	// forever, so a file stays the safe shape here.
	logPath := filepath.Join(dir, "daemon.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	cmd := exec.Command(bin, "-serve", "-socket", sock, "-token-fd", "0")
	cmd.Stdin = strings.NewReader(token + "\n")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Reproduce the trigger: the ambient reference marker is present in the
	// launcher's env (as a surrounding claude-ssh session would export it), while
	// our private re-exec sentinel is guaranteed NOT pre-set.
	env := removeEnvKey(os.Environ(), daemonChildEnv)
	cmd.Env = append(env, daemonChildMarker+"=1")
	// The launcher reads fd 0, re-execs the detached child, then exits 0. A
	// non-zero exit means it took the child branch and died on the token read —
	// i.e. the regression.
	runErr := cmd.Run()
	_ = logFile.Close()
	if runErr != nil {
		out, _ := os.ReadFile(logPath)
		t.Fatalf("launcher exited with error (regression?): %v\ndaemon.log:\n%s", runErr, out)
	}

	// The detached daemon (reparented to init) opens the socket asynchronously.
	nc := dialWithRetry(t, sock, 10*time.Second)
	t.Cleanup(func() {
		// Shut the detached daemon down via the wire — it is not our child, so we
		// cannot Wait() it. server.shutdown also removes the socket.
		_, _ = nc.Write([]byte(`{"jsonrpc":"2.0","id":99,"method":"server.shutdown","auth":"` + token + `"}` + "\n"))
		_ = nc.Close()
	})

	// An authenticated server.ping proves three things at once: the daemon is
	// listening, it received the token over the forwarded pipe, and it accepts it.
	reply := roundTrip(t, nc, `{"jsonrpc":"2.0","id":1,"method":"server.ping","auth":"`+token+`"}`)
	if _, isErr := reply["error"]; isErr {
		t.Fatalf("authenticated ping was rejected — forwarded token did not survive daemonize: %s", mustJSON(reply))
	}
	if _, ok := reply["result"]; !ok {
		t.Fatalf("ping reply had no result: %s", mustJSON(reply))
	}
}

// buildClaustrum compiles the package under test into a temp binary, matching the
// production build's CGO_ENABLED=0.
func buildClaustrum(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "claustrum")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// dialWithRetry polls the socket until it accepts a connection or the deadline
// passes (the daemon opens it a beat after the launcher exits).
func dialWithRetry(t *testing.T, sock string, within time.Duration) net.Conn {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if nc, err := net.Dial("unix", sock); err == nil {
			return nc
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("daemon never opened the socket — it likely died on the token-read child branch (the regression)")
	return nil
}

// roundTrip writes one request line and returns the first reply (the line that
// carries an "id"), ignoring any interleaved stream frames.
func roundTrip(t *testing.T, nc net.Conn, line string) map[string]any {
	t.Helper()
	if _, err := nc.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	sc := bufio.NewScanner(nc)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if _, ok := m["id"]; ok {
			return m
		}
	}
	t.Fatalf("no reply line: %v", sc.Err())
	return nil
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// TestServeWritesRemoteServerLog pins sweep gap N1: the daemonized child's
// stdout AND stderr go to remote-server.log beside the socket, mode 0600, and
// the file OUTLIVES a graceful shutdown that removes the socket and
// daemon.token.
//
// Probe-measured against the reference at 5db5e4a: its launcher's own stdout and
// stderr are 0 bytes, the banner opens the file after any limit line, and a restart
// truncates rather than appends.
//
// Both streams matter: claustrum prints the banner to stdout and log lines to
// stderr, so redirecting only stderr would leave the banner on the terminal and
// produce a log missing the banner.
func TestServeWritesRemoteServerLog(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the real binary; skipped under -short")
	}
	bin := buildClaustrum(t)
	dir, err := os.MkdirTemp("", "cld")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	const token = "remote-log-token"

	outPath := filepath.Join(dir, "launcher.out")
	errPath := filepath.Join(dir, "launcher.err")
	outF, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	errF, err := os.Create(errPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-serve", "-socket", sock, "-token-fd", "0")
	cmd.Stdin = strings.NewReader(token + "\n")
	cmd.Stdout, cmd.Stderr = outF, errF
	cmd.Env = append(removeEnvKey(os.Environ(), daemonChildEnv), daemonChildMarker+"=1")
	if runErr := cmd.Run(); runErr != nil {
		t.Fatalf("launcher failed: %v", runErr)
	}
	_ = outF.Close()
	_ = errF.Close()

	nc := dialWithRetry(t, sock, 10*time.Second)
	reply := roundTrip(t, nc, `{"jsonrpc":"2.0","id":1,"method":"server.ping","auth":"`+token+`"}`)
	if _, ok := reply["result"]; !ok {
		t.Fatalf("ping failed: %s", mustJSON(reply))
	}

	// The launcher's own streams must be empty — everything went to the log.
	for _, p := range []string{outPath, errPath} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if len(b) != 0 {
			t.Errorf("%s = %d bytes, want 0 (the child's output belongs in remote-server.log):\n%s",
				filepath.Base(p), len(b), b)
		}
	}

	logPath := filepath.Join(dir, daemonLogName)
	fi, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("remote-server.log not created beside the socket: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("remote-server.log mode = %o, want 600", got)
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	// The open-files limit line comes first when the raise works, as in
	// f6010b97. The banner follows it. A failed raise writes no limit line.
	logLines := strings.Split(string(b), "\n")
	banner := 0
	const limitLine = "INFO  [daemon] child processes will start with an open-files limit of "
	if strings.Contains(logLines[0], limitLine) {
		banner = 1
	}
	if !strings.Contains(logLines[banner], "listening on "+sock) {
		t.Errorf("log line %d = %q, want the banner naming the socket", banner, logLines[banner])
	}
	if n := strings.Count(string(b), limitLine); n != banner {
		t.Errorf("the limit line appears %d times, want %d and only as the first line", n, banner)
	}
	// The banner of the real start ends with the pid and the instance.
	if !regexp.MustCompile(` \(pid \d+, instance [0-9a-f]{32}\)$`).MatchString(logLines[banner]) {
		t.Errorf("banner = %q, want it to end with the pid and the instance", logLines[banner])
	}
	// The real start runs the cleaner start check right after the banner. This socket is
	// not run-shaped, so the check logs why the cleaner stays off. Linux and macOS only:
	// no other system links a cleaner.
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		want := `[daemon] host cleaning off: socket path "` + sock + `" is not <root>/run/<id>/rpc.sock`
		if len(logLines) < banner+2 || !strings.HasSuffix(logLines[banner+1], want) {
			t.Errorf("log after the banner = %q, want the line %q", logLines[banner+1:], want)
		}
	}

	// Graceful shutdown removes the socket and daemon.token; the log stays.
	_, _ = nc.Write([]byte(`{"jsonrpc":"2.0","id":99,"method":"server.shutdown","auth":"` + token + `"}` + "\n"))
	_ = nc.Close()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); os.IsNotExist(err) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Error("socket survived graceful shutdown")
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Errorf("remote-server.log did not survive graceful shutdown: %v", err)
	}
}

// TestServeShutsDownOnSIGTERM: SIGTERM to a real daemon gives the three lines of a
// shutdown by signal in remote-server.log, and the daemon removes its socket. The signal
// goes to one pid only: the daemon that this test started, as its banner in the log of
// this test's own temp folder names it.
func TestServeShutsDownOnSIGTERM(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the real binary; skipped under -short")
	}
	bin := buildClaustrum(t)
	dir, err := os.MkdirTemp("", "cld")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	const token = "sigterm-token"
	cmd := exec.Command(bin, "-serve", "-socket", sock, "-token-fd", "0")
	cmd.Stdin = strings.NewReader(token + "\n")
	cmd.Env = append(removeEnvKey(os.Environ(), daemonChildEnv), daemonChildMarker+"=1")
	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		t.Fatalf("launcher failed: %v\n%s", runErr, out)
	}
	nc := dialWithRetry(t, sock, 10*time.Second)
	reply := roundTrip(t, nc, `{"jsonrpc":"2.0","id":1,"method":"server.capabilities","auth":"`+token+`"}`)
	_ = nc.Close()
	result, _ := reply["result"].(map[string]any)
	instance, _ := result["instanceId"].(string)

	logPath := filepath.Join(dir, daemonLogName)
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	// The pid comes from the banner of the daemon that answered with this instance.
	m := regexp.MustCompile(`listening on ` + regexp.QuoteMeta(sock) + ` \(pid (\d+), instance ([0-9a-f]{32})\)`).FindStringSubmatch(string(b))
	if m == nil || instance == "" || m[2] != instance {
		t.Fatalf("no banner of this daemon (instance %q) in the log:\n%s", instance, b)
	}
	pid, err := strconv.Atoi(m[1])
	if err != nil || pid < 2 {
		t.Fatalf("banner pid %q", m[1])
	}
	stopped := false
	t.Cleanup(func() {
		if !stopped { // a failed run leaves no daemon behind
			_ = exec.Command(bin, "-stop", "-socket", sock).Run()
		}
	})
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM to the daemon %d: %v", pid, err)
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if _, err := os.Stat(sock); os.IsNotExist(err) {
			stopped = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !stopped {
		t.Fatal("the socket is still there 10 s after SIGTERM")
	}
	// The cleanup line comes last. Give the daemon a moment to write it.
	var got string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		b, _ = os.ReadFile(logPath)
		if got = string(b); strings.Contains(got, "[Server] cleanup: closed") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	wantLinesInOrder(t, got,
		"[daemon] received terminated; shutting down (children will be killed)",
		"[Server] shutdown requested")
	// The connection of this test closed before the signal, but the daemon may not have
	// seen the close yet. The connection count is therefore 0 or 1.
	if !regexp.MustCompile(`\[Server\] cleanup: closed [01] connection\(s\), killed 0 child process group\(s\)\n`).MatchString(got) {
		t.Errorf("log lacks the cleanup line:\n%s", got)
	}
}

// TestOpenDaemonLogDoesNotFollowSymlink pins the surviving D8 hardening: a planted
// remote-server.log symlink is never followed. os.Rename moves the link itself to
// .old and O_EXCL creates a fresh regular file, so the symlink's target is left
// untouched (the reference, measured, follows the link and writes into the victim
// in a sticky dir). A mutant that opened the path with O_TRUNC without the rename
// would truncate the victim.
func TestOpenDaemonLogDoesNotFollowSymlink(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "s.sock")
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("VICTIM-CONTENT"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, daemonLogName)); err != nil {
		t.Fatal(err)
	}
	f := openDaemonLog(sock)
	if f == nil {
		t.Error("openDaemonLog declined instead of taking the writable-dir success path")
	} else {
		_ = f.Close()
	}
	if b, _ := os.ReadFile(victim); string(b) != "VICTIM-CONTENT" {
		t.Errorf("victim was followed/truncated: %q; want VICTIM-CONTENT untouched", b)
	}
}
