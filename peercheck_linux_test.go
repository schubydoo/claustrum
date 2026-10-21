//go:build linux

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// peerDialCmd is the peer-dial helper of this test binary on the socket sock.
func peerDialCmd(t *testing.T, sock, arg string) *exec.Cmd {
	t.Helper()
	exe, env := helperCommand(t, "peer-dial")
	cmd := exec.Command(exe, sock, arg)
	cmd.Env = append(os.Environ(), "CLAUSTRUM_TEST_HELPER="+env["CLAUSTRUM_TEST_HELPER"])
	return cmd
}

// TestPeerCheckRefusesOtherUserNamespace: a caller in another user namespace gets
// the refusal frame and then the end of the connection. Its unauthenticated
// server.shutdown does nothing, and the daemon goes on to serve a caller in its own
// namespaces (round task011-01, cells P2, P4 and P8, Linux VM). The refused
// connection is not counted, and the log has the refusal line. The caller is a
// child of this test in a new user namespace. A host that refuses user namespaces
// skips the test. The log capture is shared, so the test reads only the refusal
// lines, and the server state for the rest.
func TestPeerCheckRefusesOtherUserNamespace(t *testing.T) {
	t.Setenv(peerCheckEnv, "1")
	if !daemonNSText().names() || daemonNSText().user == "" {
		t.Skip("this kernel gives no user namespace text, so no caller can differ by it")
	}
	logs := captureLogBuf(t)
	s, sock := bootLineServer(t, false)
	t.Cleanup(func() { s.signalShutdown(); s.closeAll(sock) })

	cmd := peerDialCmd(t, sock, `{"jsonrpc":"2.0","id":1,"method":"server.shutdown"}`)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Start(); err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EACCES) {
			t.Skipf("this host does not let the test start a process in a new user namespace: %v", err)
		}
		t.Fatalf("start the caller: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the caller failed: %v\n%s", err, errOut.String())
	}
	if want := string(peerRefusalLine("other_namespace")); out.String() != want {
		t.Errorf("the caller read %q, want %q", out.String(), want)
	}
	cl := dial(t, sock)
	if pong := string(cl.call(authed(`{"jsonrpc":"2.0","id":2,"method":"server.ping"}`))); pong != `{"jsonrpc":"2.0","id":2,"result":{"pong":true}}` {
		t.Errorf("server.ping after the refusal = %s, want the pong frame", pong)
	}
	// connSeq counts the connections past the check, right before their
	// "New connection" line.
	if n := s.connSeq.Load(); n != 1 {
		t.Errorf("the daemon counted %d connections, want 1 (the served caller only)", n)
	}
	select {
	case <-s.shutdown:
		t.Error("the refused caller stopped the daemon")
	default:
	}
	var refusals []string
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, "[Server] refused caller number ") {
			refusals = append(refusals, l)
		}
	}
	want := "WARN  [Server] refused caller number 1 (other_namespace): \"user:["
	if len(refusals) != 1 || !strings.HasPrefix(refusals[0], want) || !strings.HasSuffix(refusals[0], "; this server's namespaces are "+`"`+daemonNSText().String()+`"`) {
		t.Errorf("the refusal lines are %q, want one line %q…", refusals, want)
	}
	if n := s.peerCheck.refused.Load(); n != 1 {
		t.Errorf("refused = %d, want 1", n)
	}
}

// TestPeerCheckLookupOfOwnAndExitedCaller: a caller in this process has the own
// text and is served. A caller that exited before the check gets lookup_failed
// and no log line (round task011-01, cell P7, Linux VM). The exited caller is a
// child of this test that connects to the test's own socket and exits before the
// accept.
func TestPeerCheckLookupOfOwnAndExitedCaller(t *testing.T) {
	own := daemonNSText()
	if !own.names() {
		t.Skip("this kernel gives no own text")
	}
	sock := filepath.Join(shortTempDir(t), "p.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	logs := captureLogBuf(t)
	g := &peerGate{own: own}

	cli, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	nc, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	if peer, exited, err := lookupPeer(nc, own); err != nil || exited || peer != own {
		t.Errorf("lookupPeer of this process = (%q, %v, %v), want (%q, false, nil)", peer.String(), exited, err, own.String())
	}
	if !g.admit(nc) {
		t.Error("admit refused a caller in this process")
	}
	_ = nc.Close()

	if out, err := peerDialCmd(t, sock, "exit").CombinedOutput(); err != nil {
		t.Fatalf("the caller failed: %v\n%s", err, out)
	}
	nc, err = ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	peer, exited, lookErr := lookupPeer(nc, own)
	if reason, _ := judgePeer(own, peer, lookErr); !exited || reason != "lookup_failed" {
		t.Errorf("lookupPeer of an exited caller = (%q, %v, %v), reason %q, want exited and lookup_failed", peer.String(), exited, lookErr, reason)
	}
	if g.admit(nc) {
		t.Error("admit served a caller that exited")
	}
	if n := g.refused.Load(); n != 1 {
		t.Errorf("refused = %d, want 1", n)
	}
	if strings.Contains(logs.String(), "refused caller") {
		t.Errorf("an exited caller wrote a line:\n%s", logs.String())
	}
}

// peerNetnsResult is what the peer-netns fixture prints, as one JSON object.
type peerNetnsResult struct {
	Skip      string `json:"skip,omitempty"`
	OwnUser   string `json:"ownUser"`
	OwnNet    string `json:"ownNet"`
	OwnPidfd  bool   `json:"ownPidfd"`
	PeerUser  string `json:"peerUser"`
	PeerNet   string `json:"peerNet"`
	PeerPidfd bool   `json:"peerPidfd"`
	LookErr   string `json:"lookErr"`
	Reason    string `json:"reason"`
	Refusal   string `json:"refusal"` // what the caller of the daemon read
	Stopped   bool   `json:"stopped"` // the caller's server.shutdown stopped the daemon
	Pong      string `json:"pong"`
	Refused   int64  `json:"refused"`
	Conns     uint64 `json:"conns"`
}

// peerNetnsFixture runs in a new user namespace in which it is root. It starts
// peer-dial callers in a new network namespace only, so they share its user
// namespace. First it looks up one caller on a plain socket, then it serves a
// daemon with the peer check to another caller. It prints a peerNetnsResult.
func peerNetnsFixture() int {
	res, err := runPeerNetns()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	b, err := json.Marshal(res)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_, _ = os.Stdout.Write(append(b, '\n'))
	return 0
}

// startNetnsDialer starts the peer-dial helper on sock in a new network namespace.
// refused is true when the kernel does not let it make that namespace.
func startNetnsDialer(ctx context.Context, sock, arg string) (cmd *exec.Cmd, out *bytes.Buffer, refused bool, err error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, nil, false, err
	}
	cmd = exec.CommandContext(ctx, exe, sock, arg)
	cmd.Env = append(os.Environ(), "CLAUSTRUM_TEST_HELPER=peer-dial")
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNET}
	out = &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = out, os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, cloneRefused(err), err
	}
	return cmd, out, false, nil
}

// cloneRefused tells if err is the answer of a kernel that refuses a new namespace.
func cloneRefused(err error) bool {
	return errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.ENOSYS)
}

func runPeerNetns() (peerNetnsResult, error) {
	var res peerNetnsResult
	own := daemonNSText()
	res.OwnUser, res.OwnNet, res.OwnPidfd = own.user, own.net, own.pidfd
	if own.user == "" || own.net == "" {
		res.Skip = "this kernel gives no user or no net part, own text " + strconv.Quote(own.String())
		return res, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "cl")
	if err != nil {
		return res, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	// The lookup on a plain socket. The caller writes a line and then reads until
	// the close below, so it is alive at the lookup.
	ln, err := net.Listen("unix", filepath.Join(dir, "p.sock"))
	if err != nil {
		return res, err
	}
	defer func() { _ = ln.Close() }()
	cmd, _, refused, err := startNetnsDialer(ctx, filepath.Join(dir, "p.sock"), "{}")
	if refused {
		res.Skip = fmt.Sprintf("this host does not let the test start a process in a new network namespace: %v", err)
		return res, nil
	}
	if err != nil {
		return res, err
	}
	_ = ln.(*net.UnixListener).SetDeadline(time.Now().Add(10 * time.Second))
	nc, err := ln.Accept()
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return res, fmt.Errorf("accept the caller: %w", err)
	}
	peer, _, lookErr := lookupPeer(nc, own)
	res.PeerUser, res.PeerNet, res.PeerPidfd = peer.user, peer.net, peer.pidfd
	if lookErr != nil {
		res.LookErr = lookErr.Error()
	}
	res.Reason, _ = judgePeer(own, peer, lookErr)
	_ = nc.Close()
	if err := cmd.Wait(); err != nil {
		return res, fmt.Errorf("the caller of the lookup failed: %w", err)
	}

	// The daemon with the peer check, through its accept loop.
	if err := os.Setenv(peerCheckEnv, "1"); err != nil {
		return res, err
	}
	sock := filepath.Join(dir, "s.sock")
	s, err := newServerOnSocket(sock, testToken, "", wireLogOptions{}, false, false)
	if err != nil {
		return res, err
	}
	defer s.procs.killAll()
	defer func() { _ = s.ln.Close() }()
	defer func() { s.signalShutdown(); s.closeAll(sock) }()
	s.startAcceptLoops()
	cmd, out, _, err := startNetnsDialer(ctx, sock, `{"jsonrpc":"2.0","id":1,"method":"server.shutdown"}`)
	if err != nil {
		return res, err
	}
	if err := cmd.Wait(); err != nil {
		return res, fmt.Errorf("the caller of the daemon failed: %w", err)
	}
	res.Refusal = out.String()
	select {
	case <-s.shutdown:
		res.Stopped = true
	default:
	}
	cl, err := net.DialTimeout("unix", sock, 10*time.Second)
	if err != nil {
		return res, err
	}
	defer func() { _ = cl.Close() }()
	_ = cl.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := cl.Write([]byte(authed(`{"jsonrpc":"2.0","id":2,"method":"server.ping"}`) + "\n")); err != nil {
		return res, err
	}
	pong, err := bufio.NewReader(cl).ReadString('\n')
	if err != nil {
		return res, fmt.Errorf("read the pong: %w", err)
	}
	res.Pong = strings.TrimSuffix(pong, "\n")
	res.Refused = s.peerCheck.refused.Load()
	res.Conns = s.connSeq.Load()
	return res, nil
}

// TestPeerCheckRefusesOtherNetworkNamespace: a caller that shares the user
// namespace of the daemon but has its own network namespace gets
// other_namespace. Its text differs from the own text in the net part only. Through
// the accept loop it reads the refusal frame, and its server.shutdown does not stop
// the daemon. The daemon is a fixture in a new user namespace in which it is root,
// so it can start the callers in a new network namespace with no root on the host.
// A host that refuses either namespace skips the test.
func TestPeerCheckRefusesOtherNetworkNamespace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	exe, env := helperCommand(t, "peer-netns")
	cmd := exec.CommandContext(ctx, exe)
	cmd.Env = append(os.Environ(), "CLAUSTRUM_TEST_HELPER="+env["CLAUSTRUM_TEST_HELPER"])
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Start(); err != nil {
		if cloneRefused(err) {
			t.Skipf("this host does not let the test start a process in a new user namespace: %v", err)
		}
		t.Fatalf("start the fixture: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the fixture failed: %v\nstdout:\n%s\nstderr:\n%s", err, out.String(), errOut.String())
	}
	var res peerNetnsResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("the fixture printed %q: %v\nstderr:\n%s", out.String(), err, errOut.String())
	}
	if res.Skip != "" {
		t.Skip(res.Skip)
	}
	t.Logf("own text %q %q, caller text %q %q", res.OwnUser, res.OwnNet, res.PeerUser, res.PeerNet)
	if res.Reason != peerReasonOther {
		t.Errorf("reason = %q (lookup error %q), want %q", res.Reason, res.LookErr, peerReasonOther)
	}
	if res.PeerUser != res.OwnUser {
		t.Errorf("the user part of the caller is %q, want the own %q", res.PeerUser, res.OwnUser)
	}
	if res.PeerPidfd != res.OwnPidfd {
		t.Errorf("the pidfd part of the caller is %v, want the own %v", res.PeerPidfd, res.OwnPidfd)
	}
	if !strings.HasPrefix(res.PeerNet, "net:") || res.PeerNet == res.OwnNet {
		t.Errorf("the net part of the caller is %q, want another cookie than the own %q", res.PeerNet, res.OwnNet)
	}
	if want := string(peerRefusalLine(peerReasonOther)); res.Refusal != want {
		t.Errorf("the caller of the daemon read %q, want %q", res.Refusal, want)
	}
	if res.Stopped {
		t.Error("the refused caller stopped the daemon")
	}
	if want := `{"jsonrpc":"2.0","id":2,"result":{"pong":true}}`; res.Pong != want {
		t.Errorf("server.ping after the refusal = %s, want %s", res.Pong, want)
	}
	if res.Refused != 1 || res.Conns != 1 {
		t.Errorf("refused = %d and connections = %d, want 1 and 1", res.Refused, res.Conns)
	}
}
