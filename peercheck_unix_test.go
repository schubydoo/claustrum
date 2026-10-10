//go:build unix

package main

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPeerCheckReachesTheDaemonizedChild: the launcher hands CLAUDE_SSH_PEER_CHECK=1
// to the daemon that it starts. The daemon writes its one line directly before the
// listening line and answers "unavailable". On macOS that place is the one of
// 5fd08069 (macOS VM). The daemon is the one that this test started, on a socket
// in the test's own temp folder, and the test stops it over that socket.
func TestPeerCheckReachesTheDaemonizedChild(t *testing.T) {
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
	const token = "peer-check-token"
	cmd := exec.Command(bin, "-serve", "-socket", sock, "-token-fd", "0")
	cmd.Stdin = strings.NewReader(token + "\n")
	env := removeEnvKey(removeEnvKey(os.Environ(), daemonChildEnv), peerCheckEnv)
	cmd.Env = append(env, peerCheckEnv+"=1")
	// The stop is unauthenticated server.shutdown on this test's own socket. It runs
	// on every exit of the test, so a failed assertion leaves no daemon.
	t.Cleanup(func() {
		nc, dialErr := net.Dial("unix", sock)
		if dialErr != nil {
			return
		}
		_, _ = nc.Write([]byte(`{"jsonrpc":"2.0","id":99,"method":"server.shutdown"}` + "\n"))
		_ = nc.Close()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, statErr := os.Stat(sock); os.IsNotExist(statErr) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Error("the daemon did not remove its socket within 10 s of server.shutdown")
	})
	// The launcher gets a file for its output, not a pipe, in the form of
	// TestServeDaemonizeWithAmbientChildMarker.
	outPath := filepath.Join(dir, "launcher.out")
	outFile, err := os.Create(outPath)
	if err != nil {
		t.Fatalf("create launcher output: %v", err)
	}
	cmd.Stdout = outFile
	cmd.Stderr = outFile
	runErr := cmd.Run()
	_ = outFile.Close()
	if runErr != nil {
		out, _ := os.ReadFile(outPath)
		t.Fatalf("launcher failed: %v\n%s", runErr, out)
	}
	nc := dialWithRetry(t, sock, 10*time.Second)
	reply := roundTrip(t, nc, `{"jsonrpc":"2.0","id":1,"method":"server.capabilities","auth":"`+token+`"}`)
	_ = nc.Close()
	result, _ := reply["result"].(map[string]any)
	if got, _ := result["peerCheck"].(string); got != "unavailable" {
		t.Errorf("peerCheck = %q, want \"unavailable\": %s", got, mustJSON(reply))
	}

	b, err := os.ReadFile(filepath.Join(dir, daemonLogName))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	banner := -1
	for i, line := range lines {
		if strings.Contains(line, "remote server listening on "+sock) {
			banner = i
			break
		}
	}
	want := strings.TrimSuffix(wantPeerCheckLine(), "\n")
	if banner < 1 || !strings.HasSuffix(lines[banner-1], want) {
		t.Errorf("want the line %q directly before the listening line. Log:\n%s", want, b)
	}
	if n := strings.Count(string(b), want); n != 1 {
		t.Errorf("the peer check line appears %d times, want 1. Log:\n%s", n, b)
	}
}

// TestGitChildDoesNotInheritPeerCheck: after the start of the daemon, the git calls
// of a served git.info get no CLAUDE_SSH_PEER_CHECK. On 5fd08069 no git call of one
// git.info had it for the daemon value 0 (Linux VM). A marker variable of the
// daemon is the control: it arrives in every call. The stand-in git is this test
// binary behind a PATH link named git. It records its environment and fails.
func TestGitChildDoesNotInheritPeerCheck(t *testing.T) {
	const marker = "CLAUSTRUM_TEST_PEER_MARK"
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	bin := t.TempDir()
	if err := os.Symlink(exe, filepath.Join(bin, "git")); err != nil {
		t.Fatalf("symlink git: %v", err)
	}
	envLog := filepath.Join(t.TempDir(), "env.log")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAUSTRUM_TEST_HELPER", "git-envlog")
	t.Setenv("CLAUSTRUM_GITSTUB_ENVLOG", envLog)
	t.Setenv("CLAUSTRUM_GITSTUB_ENVNAMES", peerCheckEnv+" "+marker)
	t.Setenv(marker, "1")
	t.Setenv(peerCheckEnv, "0")

	s := newTestServer(t)
	s.peerCheckAsked = startPeerCheck()
	dispatchRaw(t, s, rpcLine(t, "git.info", map[string]any{"path": t.TempDir()}))

	b, err := os.ReadFile(envLog)
	if err != nil {
		t.Fatalf("the stand-in git recorded no call: %v", err)
	}
	want := peerCheckEnv + " absent;" + marker + " set 1;"
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	for i, line := range lines {
		if line != want {
			t.Errorf("git call %d of %d saw %q, want %q", i+1, len(lines), line, want)
		}
	}
}
