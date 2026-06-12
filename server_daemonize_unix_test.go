//go:build unix

package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	// The daemonized child inherits the launcher's stdout/stderr (daemonizeWithToken
	// sets cmd.Stdout = os.Stdout) and holds them open for its whole life, so we
	// must NOT use a pipe (CombinedOutput) here — Wait would block on EOF forever.
	// Redirect stdio to a file, exactly as clauster's spawn does (→ daemon.log).
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
