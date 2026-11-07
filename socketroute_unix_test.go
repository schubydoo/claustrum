//go:build unix

package main

import (
	"bufio"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/sockroute"
	"github.com/schubydoo/claustrum/internal/tokenfile"
	"github.com/schubydoo/claustrum/internal/wirelog"
)

// longSocketPath returns a path of exactly total bytes under a fresh temp folder
// that ends in base. base can hold slashes. The folder of the path exists.
func longSocketPath(t *testing.T, total int, base string) string {
	t.Helper()
	root := shortTempDir(t)
	pad := total - len(root) - len(base) - 2
	if pad < 1 || pad > 255 {
		t.Fatalf("cannot build a %d-byte path under %s with base %d bytes", total, root, len(base))
	}
	p := filepath.Join(root, strings.Repeat("d", pad), base)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if len(p) != total {
		t.Fatalf("built a %d-byte path, want %d", len(p), total)
	}
	return p
}

// plugins.prune derives its plugin root from the path of the start, not from the
// shorter name that a long path was bound by.
func TestSocketPathOfALongSocket(t *testing.T) {
	sock := longSocketPath(t, 120, "run/abcd1234/rpc.sock")
	s, err := newServerOnSocket(sock, "long-tok", "", wirelog.Options{}, false, false)
	if err != nil {
		t.Fatalf("newServerOnSocket at a %d-byte path: %v", len(sock), err)
	}
	t.Cleanup(func() {
		s.signalShutdown()
		s.closeAll(sock)
	})
	if got := s.socketPath(); got != sock {
		t.Fatalf("socketPath = %q, want %q", got, sock)
	}
	if _, _, _, ok := pluginsLayoutFromSocket(s.socketPath()); !ok {
		t.Errorf("no plugin root for %s", sock)
	}
}

// The teardown at a long path removes rpc.sock and daemon.token, as at a short
// path. `be1c1ca5` does that after SIGTERM and after -stop at 120 bytes (measured
// on Linux).
func TestCloseAllAtALongSocketPath(t *testing.T) {
	sock := longSocketPath(t, 120, "rpc.sock")
	s, err := newServerOnSocket(sock, "long-tok", "", wirelog.Options{}, false, false)
	if err != nil {
		t.Fatalf("newServerOnSocket: %v", err)
	}
	token := filepath.Join(filepath.Dir(sock), tokenfile.Name)
	for _, p := range []string{sock, token} {
		if _, err := os.Lstat(p); err != nil {
			t.Fatalf("%s missing before the teardown: %v", p, err)
		}
	}
	s.signalShutdown()
	s.closeAll(sock)
	for _, p := range []string{sock, token} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s stays after the teardown: %v", p, err)
		}
	}
}

// -serve, -bridge and -stop work at a 120-byte socket path, and -stop leaves no
// rpc.sock or daemon.token. `be1c1ca5` does that on Linux (120 bytes) and macOS
// (110 bytes), measured.
func TestServeBridgeStopAtALongSocketPath(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the real binary; skipped under -short")
	}
	bin := buildClaustrum(t)
	sock := longSocketPath(t, 120, "rpc.sock")
	tokFile := filepath.Join(shortTempDir(t), "tok")
	if err := os.WriteFile(tokFile, []byte("tok-long"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "CLAUDE_SSH_DAEMON_CHILD=")
	serve := exec.Command(bin, "-serve", "-socket", sock, "-token-file", tokFile)
	serve.Env = env
	// The cleanup comes before the start, so a start that fails after the bind
	// leaves no daemon.
	t.Cleanup(func() {
		if c, err := sockroute.DialUnixPath(sock, time.Second); err == nil {
			_, _ = io.WriteString(c, `{"jsonrpc":"2.0","id":1,"method":"server.shutdown"}`+"\n")
			_ = c.Close()
		}
	})
	if out, err := serve.CombinedOutput(); err != nil {
		t.Fatalf("-serve at a 120-byte path: %v\n%s", err, out)
	}

	// The bridge keeps stdin open until the reply is read: a stdin EOF before the
	// reply ends the relay with no output.
	bridge := exec.Command(bin, "-bridge", "-socket", sock)
	bridge.Env = env
	in, err := bridge.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	outPipe, err := bridge.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.Start(); err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(in, `{"jsonrpc":"2.0","id":9,"method":"server.ping","auth":"tok-long"}`+"\n")
	line, rerr := bufio.NewReader(outPipe).ReadString('\n')
	_ = in.Close()
	werr := bridge.Wait()
	if rerr != nil || werr != nil || !strings.HasPrefix(line, `{"jsonrpc":"2.0","id":9,"result":{"pong":true}`) {
		t.Fatalf("-bridge ping: %v, %v, %q", rerr, werr, line)
	}

	stop := exec.Command(bin, "-stop", "-socket", sock)
	stop.Env = env
	if out, err := stop.Output(); err != nil || string(out) != "stopped\n" {
		t.Fatalf("-stop: %v, %q", err, out)
	}
	for _, p := range []string{sock, filepath.Join(filepath.Dir(sock), tokenfile.Name)} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s stays after -stop: %v", p, err)
		}
	}
}

// The launcher reads the pid of a daemon that answers at a long path, so its wait
// does not take that daemon for the new one.
func TestLivePredecessorPIDAtALongSocketPath(t *testing.T) {
	sock := longSocketPath(t, 120, "rpc.sock")
	ln, err := sockroute.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	held := make(chan net.Conn, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			held <- c
		}
	}()
	t.Cleanup(func() {
		select {
		case c := <-held:
			_ = c.Close()
		default:
		}
	})
	if got := livePredecessorPID(sock); got != os.Getpid() {
		t.Errorf("livePredecessorPID = %d, want the pid of the listener %d", got, os.Getpid())
	}
}

// A launcher connect that cannot change back to the working folder ends the
// launcher with exit 1 (claustrum's own).
func TestLivePredecessorPIDChangeBackFails(t *testing.T) {
	noFolderHandle(t)
	sock := longSocketPath(t, 120, "rpc.sock")
	old := sockroute.SetChangeBackForTest(func(back *os.File) error {
		_ = back.Chdir() // keep the test process in its folder
		return errors.New("stub")
	})
	t.Cleanup(func() { sockroute.SetChangeBackForTest(old) })
	stubOsExit(t)
	code, exited := catchExit(func() { livePredecessorPID(sock) })
	if !exited || code != 1 {
		t.Errorf("exited=%v code=%d, want exit 1", exited, code)
	}
}

// The launcher wait ends with exit 1 too when its connect cannot change back.
func TestWaitForDaemonAcceptChangeBackFails(t *testing.T) {
	noFolderHandle(t)
	sock := longSocketPath(t, 120, "rpc.sock")
	old := sockroute.SetChangeBackForTest(func(back *os.File) error {
		_ = back.Chdir() // keep the test process in its folder
		return errors.New("stub")
	})
	t.Cleanup(func() { sockroute.SetChangeBackForTest(old) })
	stubOsExit(t)
	code, exited := catchExit(func() { waitForDaemonAccept(sock, 0) })
	if !exited || code != 1 {
		t.Errorf("exited=%v code=%d, want exit 1", exited, code)
	}
}
