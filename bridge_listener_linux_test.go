//go:build linux

package main

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// dialOwnListener dials a listener of this process and returns the connection.
func dialOwnListener(t *testing.T) net.Conn {
	t.Helper()
	sock := adoptSocketDir(t, "")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nc.Close() })
	return nc
}

// fakeNamespaces makes the reads of the check give the links of a peer with user
// and netns, against the own links user:[4026531837] and net:[4026531840].
func fakeNamespaces(t *testing.T, user, netns string) {
	t.Helper()
	old := listenerReadlink
	t.Cleanup(func() { listenerReadlink = old })
	listenerReadlink = func(path string) (string, error) {
		own := strings.HasPrefix(path, "/proc/self/")
		switch {
		case strings.HasSuffix(path, "/ns/user") && own:
			return "user:[4026531837]", nil
		case strings.HasSuffix(path, "/ns/user"):
			return user, nil
		case own:
			return "net:[4026531840]", nil
		}
		return netns, nil
	}
}

// TestListenerAnswerOwnNamespaces: a listener in the own namespaces answers on
// (13047a51, measured on Linux).
func TestListenerAnswerOwnNamespaces(t *testing.T) {
	for _, link := range []string{"/proc/self/ns/user", "/proc/self/ns/net"} {
		if _, err := os.Readlink(link); err != nil {
			t.Skipf("no namespace link here: %v", err)
		}
	}
	answer, refusal := listenerAnswer(dialOwnListener(t))
	if refusal != nil || answer != listenerAnswerOn {
		t.Errorf("listenerAnswer = %q, %v, want on", answer, refusal)
	}
}

// TestListenerAnswerOtherNetNamespace: a listener in another net namespace is
// refused with other_namespace, in the text of 13047a51 (measured on Linux).
func TestListenerAnswerOtherNetNamespace(t *testing.T) {
	fakeNamespaces(t, "user:[4026531837]", "net:[4026532301]")
	_, refusal := listenerAnswer(dialOwnListener(t))
	want := fmt.Sprintf("refusing the listener (other_namespace): process %d listens in user:[4026531837] net:[4026532301], not user:[4026531837] net:[4026531840]; nothing was sent to it", os.Getpid())
	if refusal == nil || refusal.Error() != want {
		t.Errorf("listenerAnswer refusal = %v, want %q", refusal, want)
	}
}

// TestListenerAnswerOtherUserNamespace: another user namespace with the same net
// namespace is refused too (13047a51, measured on Linux).
func TestListenerAnswerOtherUserNamespace(t *testing.T) {
	fakeNamespaces(t, "user:[4026532400]", "net:[4026531840]")
	_, refusal := listenerAnswer(dialOwnListener(t))
	want := fmt.Sprintf("refusing the listener (other_namespace): process %d listens in user:[4026532400] net:[4026531840], not user:[4026531837] net:[4026531840]; nothing was sent to it", os.Getpid())
	if refusal == nil || refusal.Error() != want {
		t.Errorf("listenerAnswer refusal = %v, want %q", refusal, want)
	}
}

// TestBridgeValueOneRefusesOtherNetNamespace: value 1 refuses a daemon in another
// net namespace, and main exits 79 (13047a51, measured on Linux). The listener
// gets nothing (claustrum's own on Linux).
func TestBridgeValueOneRefusesOtherNetNamespace(t *testing.T) {
	fakeNamespaces(t, "user:[4026531837]", "net:[4026532301]")
	t.Setenv(bridgeListenerEnv, "1")
	sock, got := fakeListener(t, "")
	if code, exited := runMain(t, "-bridge", "-socket", sock); !exited || code != 79 {
		t.Errorf("main -bridge with value 1 and another net namespace: exit %d (exited %v), want 79", code, exited)
	}
	if b := <-got; len(b) != 0 {
		t.Errorf("the listener got %q, want nothing", b)
	}
}

// fakePeer makes the check read the peer pid and uid from cred.
func fakePeer(t *testing.T, cred unix.Ucred) {
	t.Helper()
	old := listenerPeerCred
	t.Cleanup(func() { listenerPeerCred = old })
	listenerPeerCred = func(net.Conn) (*unix.Ucred, error) { return &cred, nil }
}

// TestListenerAnswerOtherUser: a listener of another uid is refused with
// other_user, value 1 included, and main exits 79 (13047a51, measured on Linux
// with a daemon of uid 1). The listener gets nothing (claustrum's own on Linux).
func TestListenerAnswerOtherUser(t *testing.T) {
	other := uint32(os.Geteuid()) + 1
	fakePeer(t, unix.Ucred{Pid: int32(os.Getpid()), Uid: other})
	var stderr bytes.Buffer
	err := judgeSocketOwner(dialOwnListener(t), "1", true, &stderr)
	want := fmt.Sprintf("refusing the listener (other_user): user %d listens; nothing was sent to it", other)
	if err == nil || err.Error() != want {
		t.Errorf("value 1 with another uid: %v, want %q", err, want)
	}
	if stderr.Len() != 0 {
		t.Errorf("value 1 with another uid: stderr %q, want none", stderr.String())
	}
	t.Setenv(bridgeListenerEnv, "1")
	sock, got := fakeListener(t, "")
	if code, exited := runMain(t, "-bridge", "-socket", sock); !exited || code != 79 {
		t.Errorf("main -bridge with value 1 and another uid: exit %d (exited %v), want 79", code, exited)
	}
	if b := <-got; len(b) != 0 {
		t.Errorf("the listener got %q, want nothing", b)
	}
}

// TestFailedReadsAnswerUnavailable: a failed read of the peer or of the own
// namespaces answers unavailable. Value 1 then passes, and strict refuses with
// too_weak (claustrum's own, not measured). A client in another pid namespace
// gets unavailable on 13047a51 (measured on Linux).
func TestFailedReadsAnswerUnavailable(t *testing.T) {
	oldCred, oldLink := listenerPeerCred, listenerReadlink
	t.Cleanup(func() { listenerPeerCred, listenerReadlink = oldCred, oldLink })
	for _, tc := range []struct {
		name string
		set  func()
	}{
		{"peer", func() {
			listenerPeerCred = func(net.Conn) (*unix.Ucred, error) { return nil, errors.New("seam: no peer") }
			listenerReadlink = oldLink
		}},
		{"peer pid 0", func() {
			listenerPeerCred = func(net.Conn) (*unix.Ucred, error) {
				return &unix.Ucred{Pid: 0, Uid: uint32(os.Geteuid())}, nil
			}
			listenerReadlink = oldLink
		}},
		{"own namespaces", func() {
			listenerPeerCred = oldCred
			listenerReadlink = func(path string) (string, error) {
				if strings.HasPrefix(path, "/proc/self/") {
					return "", errors.New("seam: no link")
				}
				return oldLink(path)
			}
		}},
	} {
		tc.set()
		nc := dialOwnListener(t)
		var stderr bytes.Buffer
		if err := judgeSocketOwner(nc, "1", true, &stderr); err != nil {
			t.Errorf("%s read fails, value 1: %v, want a pass", tc.name, err)
		}
		if got, want := stderr.String(), "claustrum: listener check (unavailable)\n"; got != want {
			t.Errorf("%s read fails, value 1: stderr %q, want %q", tc.name, got, want)
		}
		err := judgeSocketOwner(nc, "strict", true, &stderr)
		var refused *ownerRejection
		if !errors.As(err, &refused) || refused.kind != "too_weak" {
			t.Errorf("%s read fails, strict: %v, want the too_weak refusal", tc.name, err)
		}
	}
}

// TestListenerAnswerUnreadable: a failed read of the namespaces of the listener
// refuses with unreadable and the error of the read, value 1 included, and main
// exits 79 (13047a51, measured on Linux with a client in another user namespace).
// The listener gets nothing (claustrum's own on Linux).
func TestListenerAnswerUnreadable(t *testing.T) {
	old := listenerReadlink
	t.Cleanup(func() { listenerReadlink = old })
	listenerReadlink = func(path string) (string, error) {
		if !strings.HasPrefix(path, "/proc/self/") {
			return "", &os.PathError{Op: "readlink", Path: path, Err: unix.EACCES}
		}
		return "user:[4026531837]", nil
	}
	var stderr bytes.Buffer
	err := judgeSocketOwner(dialOwnListener(t), "1", true, &stderr)
	want := fmt.Sprintf("refusing the listener (unreadable): readlink /proc/%d/ns/user: permission denied; nothing was sent to it", os.Getpid())
	if err == nil || err.Error() != want {
		t.Errorf("value 1 with an unreadable listener: %v, want %q", err, want)
	}
	if stderr.Len() != 0 {
		t.Errorf("value 1 with an unreadable listener: stderr %q, want none", stderr.String())
	}
	t.Setenv(bridgeListenerEnv, "1")
	sock, got := fakeListener(t, "")
	if code, exited := runMain(t, "-bridge", "-socket", sock); !exited || code != 79 {
		t.Errorf("main -bridge with value 1 and an unreadable listener: exit %d (exited %v), want 79", code, exited)
	}
	if b := <-got; len(b) != 0 {
		t.Errorf("the listener got %q, want nothing", b)
	}
}

// TestListenerAnswerOtherUserBeforeReads: a listener of another uid whose links
// cannot be read gets other_user, not unreadable. With a peer pid of 0 it gets
// other_user too. A daemon of another uid gets other_user and not unreadable on
// 13047a51 (measured on Linux with a daemon of uid 1). The order of the tests in
// claustrum's code is claustrum's own, not measured.
func TestListenerAnswerOtherUserBeforeReads(t *testing.T) {
	old := listenerReadlink
	t.Cleanup(func() { listenerReadlink = old })
	listenerReadlink = func(path string) (string, error) {
		if !strings.HasPrefix(path, "/proc/self/") {
			return "", &os.PathError{Op: "readlink", Path: path, Err: unix.EACCES}
		}
		return "user:[4026531837]", nil
	}
	other := uint32(os.Geteuid()) + 1
	want := fmt.Sprintf("refusing the listener (other_user): user %d listens; nothing was sent to it", other)
	for _, pid := range []int32{int32(os.Getpid()), 0} {
		fakePeer(t, unix.Ucred{Pid: pid, Uid: other})
		var stderr bytes.Buffer
		err := judgeSocketOwner(dialOwnListener(t), "1", true, &stderr)
		if err == nil || err.Error() != want {
			t.Errorf("peer pid %d of another uid, unreadable links: %v, want %q", pid, err, want)
		}
	}
}
