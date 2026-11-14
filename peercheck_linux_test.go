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

	"golang.org/x/sys/unix"

	"github.com/schubydoo/claustrum/internal/wirelog"
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
// namespaces (measured on Linux). The refused connection is not counted, and the
// log has the refusal line. The caller is a child of this test in a new user
// namespace. A host that refuses user namespaces skips the test. The log capture is
// shared, so the test reads only the refusal lines, and the server state for the
// rest.
func TestPeerCheckRefusesOtherUserNamespace(t *testing.T) {
	t.Setenv(peerCheckEnv, "1")
	if own, _ := daemonNSText(); !own.names() || own.user == "" {
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
	if len(refusals) != 1 || !strings.HasPrefix(refusals[0], want) || !strings.HasSuffix(refusals[0], "; this server's namespaces are "+`"`+s.peerCheck.ownText().String()+`"`) {
		t.Errorf("the refusal lines are %q, want one line %q…", refusals, want)
	}
	if n := s.peerCheck.refused.Load(); n != 1 {
		t.Errorf("refused = %d, want 1", n)
	}
}

// TestPeerCheckLookupOfOwnAndExitedCaller: a caller in this process has the own
// text and is served. A caller that exited before the check gets lookup_failed
// and no log line, and it counts (measured on Linux). The exited caller is a
// child of this test that connects to the test's own socket and exits before
// the accept.
func TestPeerCheckLookupOfOwnAndExitedCaller(t *testing.T) {
	own, _ := daemonNSText()
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

// TestPeerCheckPidfdErrorNamesCaller: a SO_PEERPIDFD read that answers EMFILE
// refuses the caller with lookup_failed. The line names the caller by its pid
// (measured on Linux, c9125eb8 and 4c111e86). The caller is this process.
func TestPeerCheckPidfdErrorNamesCaller(t *testing.T) {
	own := nsText{user: "user:[1]", net: "net:1", pidfd: true}
	old := peerPidfdOf
	t.Cleanup(func() { peerPidfdOf = old })
	peerPidfdOf = func(int) (int, error) { return -1, syscall.EMFILE }

	sock := filepath.Join(shortTempDir(t), "p.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	cli, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	nc, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	logs := captureLogBuf(t)
	g := &peerGate{own: own}
	if g.admit(nc) {
		t.Fatal("admit served a caller whose pidfd read failed")
	}
	want := fmt.Sprintf("[Server] refused caller number 1 (lookup_failed): caller %d: too many open files; this server's namespaces are %q", os.Getpid(), own.String())
	if !strings.Contains(logs.String(), want) {
		t.Errorf("log lacks %q:\n%s", want, logs.String())
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
	own, err := daemonNSText()
	if err != nil {
		return res, err
	}
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
	s, err := newServerOnSocket(sock, testToken, "", wirelog.Options{}, false, false)
	if err != nil {
		return res, err
	}
	defer s.procs.KillAll()
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
	if want := `{"jsonrpc":"2.0","id":2,"result":{"pong":true},"end":{"at":0,"len":46}}`; res.Pong != want {
		t.Errorf("server.ping after the refusal = %s, want %s", res.Pong, want)
	}
	if res.Refused != 1 || res.Conns != 1 {
		t.Errorf("refused = %d and connections = %d, want 1 and 1", res.Refused, res.Conns)
	}
}

// TestPeerLookupStepErrors: an errno at each step of the lookup gives the reason
// and the text of 4c111e86 (measured on Linux). ENFILE and ENOMEM give
// out_of_resources. A failed own naming gives lookup_failed for EACCES. A gone own
// user link gives the own text with no user part. A gone caller link sends the
// check to /proc/<pid>/ns. The caller is this process.
func TestPeerLookupStepErrors(t *testing.T) {
	real, err := daemonNSText()
	if err != nil || real.net == "" || !real.pidfd {
		t.Skipf("this kernel gives no net or pidfd part (own text %q, %v)", real.String(), err)
	}
	own := nsText{user: "user:[1]", net: real.net, pidfd: true}
	pid := os.Getpid()
	callerLink := fmt.Sprintf("/proc/%d/ns/user", pid)
	callerDir := fmt.Sprintf("/proc/%d/ns", pid)
	enfile, enomem := syscall.ENFILE.Error(), syscall.ENOMEM.Error()
	cases := []struct {
		name           string
		cookie, cred   error
		pidfd          error
		link           map[string]error
		names          []string
		namesErr       error
		reason, detail string
		exited         bool
	}{
		{name: "cookie ENFILE", cookie: syscall.ENFILE, reason: "out_of_resources", detail: enfile},
		{name: "cred ENOMEM", cred: syscall.ENOMEM, reason: "out_of_resources", detail: enomem},
		{name: "pidfd ENFILE", pidfd: syscall.ENFILE, reason: "out_of_resources", detail: fmt.Sprintf("caller %d: %s", pid, enfile)},
		{name: "own pid ENOMEM", link: map[string]error{"/proc/self": syscall.ENOMEM}, reason: "out_of_resources", detail: "cannot name this server's own namespaces: readlink /proc/self: " + enomem},
		{name: "own user ENFILE", link: map[string]error{"/proc/self/ns/user": syscall.ENFILE}, reason: "out_of_resources", detail: "cannot name this server's own namespaces: readlink /proc/self/ns/user: " + enfile},
		{name: "own user EACCES", link: map[string]error{"/proc/self/ns/user": syscall.EACCES}, reason: "lookup_failed", detail: "cannot name this server's own namespaces: readlink /proc/self/ns/user: permission denied"},
		{name: "own user gone", link: map[string]error{"/proc/self/ns/user": syscall.ENOENT}, reason: "out_of_resources", detail: `"` + real.net + `"`},
		{name: "own pid gone", link: map[string]error{"/proc/self": syscall.ENOENT}, reason: "out_of_resources", detail: `"` + real.net + `"`},
		{name: "caller ENOMEM", link: map[string]error{callerLink: syscall.ENOMEM}, reason: "out_of_resources", detail: "readlink " + callerLink + ": " + enomem},
		{name: "caller EACCES", link: map[string]error{callerLink: syscall.EACCES}, reason: "unreadable", detail: "readlink " + callerLink + ": permission denied"},
		{name: "caller gone, user listed", link: map[string]error{callerLink: syscall.ENOENT}, names: []string{"net", "user"}, reason: "out_of_resources", detail: callerDir + ` lists "user", which cannot be read: ` + enomem},
		{name: "caller gone, no user listed", link: map[string]error{callerLink: syscall.ENOENT}, names: []string{"net"}, reason: "out_of_resources", detail: callerDir + " shows no user namespace, on a system that has them: " + enomem},
		{name: "caller exited", link: map[string]error{callerLink: syscall.ENOENT}, namesErr: &os.PathError{Op: "open", Path: callerDir, Err: syscall.ENOENT}, reason: "lookup_failed", detail: "readlink " + callerLink + ": " + syscall.ENOENT.Error(), exited: true},
	}
	sock := filepath.Join(shortTempDir(t), "p.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	cli, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	nc, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nc.Close() })
	oldLink, oldNames, oldCookie, oldCred, oldPidfd := peerReadlink, peerNSNames, peerCookieOf, peerCredOf, peerPidfdOf
	t.Cleanup(func() {
		peerReadlink, peerNSNames, peerCookieOf, peerCredOf, peerPidfdOf = oldLink, oldNames, oldCookie, oldCred, oldPidfd
	})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			peerReadlink = func(path string) (string, error) {
				if e := tc.link[path]; e != nil {
					return "", &os.PathError{Op: "readlink", Path: path, Err: e}
				}
				return oldLink(path)
			}
			peerNSNames = func(dir string) ([]string, error) {
				if tc.namesErr != nil {
					return nil, tc.namesErr
				}
				if tc.names != nil {
					return tc.names, nil
				}
				return oldNames(dir)
			}
			peerCookieOf = func(fd int) (uint64, error) {
				if tc.cookie != nil {
					return 0, tc.cookie
				}
				return oldCookie(fd)
			}
			peerCredOf = func(fd int) (*unix.Ucred, error) {
				if tc.cred != nil {
					return nil, tc.cred
				}
				return oldCred(fd)
			}
			peerPidfdOf = func(fd int) (int, error) {
				if tc.pidfd != nil {
					return -1, tc.pidfd
				}
				return oldPidfd(fd)
			}
			peer, exited, lookErr := lookupPeer(nc, own)
			reason, detail := judgePeer(own, peer, lookErr)
			if reason != tc.reason || detail != tc.detail || exited != tc.exited {
				t.Errorf("lookup = (%q, %q, exited %v), want (%q, %q, exited %v)", reason, detail, exited, tc.reason, tc.detail, tc.exited)
			}
		})
	}
}

// TestDaemonNSTextStartNaming: the start naming of the own user link. A gone link
// with "user" in /proc/self/ns and a failed read are an ownNamingError with the
// text of 4c111e86 (measured on Linux). A gone link with no "user" listed gives no
// user part and no error (measured).
func TestDaemonNSTextStartNaming(t *testing.T) {
	oldLink, oldNames := peerReadlink, peerNSNames
	t.Cleanup(func() { peerReadlink, peerNSNames = oldLink, oldNames })
	fail := func(e error) func(string) (string, error) {
		return func(path string) (string, error) {
			if path == "/proc/self/ns/user" {
				return "", &os.PathError{Op: "readlink", Path: path, Err: e}
			}
			return oldLink(path)
		}
	}
	peerReadlink = fail(syscall.ENFILE)
	if _, err := daemonNSText(); err == nil || err.Error() != "cannot name this server's own namespaces: readlink /proc/self/ns/user: "+syscall.ENFILE.Error() {
		t.Errorf("ENFILE: err = %v", err)
	}
	peerReadlink = fail(syscall.ENOENT)
	peerNSNames = func(string) ([]string, error) { return []string{"net", "user"}, nil }
	if _, err := daemonNSText(); err == nil || err.Error() != `cannot name this server's own namespaces: /proc/self/ns lists "user", which cannot be read: `+syscall.ENOMEM.Error() {
		t.Errorf("gone link, user listed: err = %v", err)
	}
	peerNSNames = func(string) ([]string, error) { return []string{"net"}, nil }
	own, err := daemonNSText()
	if err != nil || own.user != "" {
		t.Errorf("gone link, no user listed: (%q, %v), want no user part and no error", own.String(), err)
	}
	if own.net != "" && strings.Contains(own.String(), "pidfd") {
		t.Errorf("own text %q shows pidfd with no user part", own.String())
	}
}
