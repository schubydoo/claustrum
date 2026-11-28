//go:build linux

package main

import (
	"errors"
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// The reads of the listener check. A test replaces one.
var (
	listenerReadlink = os.Readlink
	listenerPeerCred = connCredOf
)

// connCredOf reads the pid and uid of the process at the other end of nc by
// SO_PEERCRED.
func connCredOf(nc net.Conn) (*unix.Ucred, error) {
	uc, ok := nc.(*net.UnixConn)
	if !ok {
		return nil, errors.New("the connection is not a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return nil, err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return nil, err
	}
	if credErr != nil {
		return nil, credErr
	}
	return cred, nil
}

// namespacesOf joins the links of the user and net namespaces of a process with
// one space, as "user:[4026531837] net:[4026531840]". That is the form of the
// texts of 13047a51 (measured on Linux).
func namespacesOf(proc string) (string, error) {
	user, err := listenerReadlink("/proc/" + proc + "/ns/user")
	if err != nil {
		return "", err
	}
	netns, err := listenerReadlink("/proc/" + proc + "/ns/net")
	if err != nil {
		return "", err
	}
	return user + " " + netns, nil
}

// listenerAnswer refuses a listener of another uid with the kind other_user, as
// 13047a51 does for a daemon of another account (measured on Linux). The use of
// the effective uid and the order before the pid test are claustrum's own, not
// measured. It then
// compares the user and net namespaces of the listener with the
// own ones. Equal ones answer "on". Other ones refuse with the kind
// other_namespace. 13047a51 refuses a daemon in another net namespace, and passes
// one in another mount, uts or ipc namespace (measured on Linux). It refuses
// another user namespace alone too (measured on Linux). A failed read of
// the namespaces of the listener refuses with the kind unreadable and the error
// text, as 13047a51 does for a client in another user namespace (measured on
// Linux). A client in another pid namespace gets "unavailable" on 13047a51
// (measured on Linux). A failed read of the peer or of the own
// namespaces answers "unavailable" too (claustrum's own, not measured).
func listenerAnswer(nc net.Conn) (string, *ownerRejection) {
	cred, err := listenerPeerCred(nc)
	if err != nil {
		return listenerAnswerUnavailable, nil
	}
	if cred.Uid != uint32(os.Geteuid()) {
		return "", &ownerRejection{kind: "other_user", detail: fmt.Sprintf("user %d listens", cred.Uid)}
	}
	pid := cred.Pid
	if pid <= 0 {
		return listenerAnswerUnavailable, nil
	}
	own, err := namespacesOf("self")
	if err != nil {
		return listenerAnswerUnavailable, nil
	}
	theirs, err := namespacesOf(fmt.Sprint(pid))
	if err != nil {
		return "", &ownerRejection{kind: "unreadable", detail: err.Error()}
	}
	if theirs != own {
		return "", &ownerRejection{kind: "other_namespace", detail: fmt.Sprintf("process %d listens in %s, not %s", pid, theirs, own)}
	}
	return listenerAnswerOn, nil
}
