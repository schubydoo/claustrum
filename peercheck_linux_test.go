//go:build linux

package main

import (
	"bytes"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
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
