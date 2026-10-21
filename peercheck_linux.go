//go:build linux

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// How the parts of the own text are read is claustrum's own, not measured. The
// user part is the link /proc/self/ns/user. The net part is SO_NETNS_COOKIE of a
// unix socket. The pidfd part is there when SO_PEERPIDFD works on a unix socket.

// daemonNSText reads the own text of the daemon. A part that the kernel does not
// give stays empty.
func daemonNSText() nsText {
	var t nsText
	if link, err := os.Readlink("/proc/self/ns/user"); err == nil {
		t.user = link
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return t
	}
	defer unix.Close(fds[0])
	defer unix.Close(fds[1])
	if cookie, err := unix.GetsockoptUint64(fds[0], unix.SOL_SOCKET, unix.SO_NETNS_COOKIE); err == nil {
		t.net = "net:" + strconv.FormatUint(cookie, 10)
	}
	if pfd, err := unix.GetsockoptInt(fds[0], unix.SOL_SOCKET, unix.SO_PEERPIDFD); err == nil {
		unix.Close(pfd)
		t.pidfd = true
	}
	return t
}

// lookupPeer builds the own text of the process at the other end of nc, with the
// parts that own has. The pid comes from SO_PEERCRED, the user part from the link
// /proc/<pid>/ns/user, the net part from the cookie of nc, and the pidfd part
// from SO_PEERPIDFD. The pidfd must not be readable yet, after the other reads.
// exited is true for a caller that is gone: a readable pidfd or no namespace link
// (claustrum's own, not measured).
func lookupPeer(nc net.Conn, own nsText) (peer nsText, exited bool, err error) {
	uc, ok := nc.(*net.UnixConn)
	if !ok {
		return peer, false, errors.New("the connection is not a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return peer, false, err
	}
	var lookErr error
	if cerr := raw.Control(func(fd uintptr) {
		peer, exited, lookErr = lookupPeerFD(int(fd), own)
	}); cerr != nil {
		return peer, false, cerr
	}
	return peer, exited, lookErr
}

func lookupPeerFD(fd int, own nsText) (peer nsText, exited bool, err error) {
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return peer, false, fmt.Errorf("SO_PEERCRED: %w", err)
	}
	if cred.Pid <= 0 {
		return peer, false, errors.New("the caller has no pid in this namespace")
	}
	pfd := -1
	if own.pidfd {
		// A Linux 6.12 host answers EINVAL for a caller that is gone (no test
		// pins the errno). ESRCH counts as gone too (claustrum's own, not measured).
		pfd, err = unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PEERPIDFD)
		if err != nil {
			gone := errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ESRCH)
			return peer, gone, fmt.Errorf("SO_PEERPIDFD: %w", err)
		}
		defer unix.Close(pfd)
	}
	if own.user != "" {
		link, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/user", cred.Pid))
		if err != nil {
			return peer, errors.Is(err, fs.ErrNotExist), err
		}
		peer.user = link
	}
	if own.net != "" {
		cookie, err := unix.GetsockoptUint64(fd, unix.SOL_SOCKET, unix.SO_NETNS_COOKIE)
		if err != nil {
			return peer, false, fmt.Errorf("SO_NETNS_COOKIE: %w", err)
		}
		peer.net = "net:" + strconv.FormatUint(cookie, 10)
	}
	if pfd >= 0 {
		fds := []unix.PollFd{{Fd: int32(pfd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 0)
		if err != nil {
			return peer, false, fmt.Errorf("poll pidfd: %w", err)
		}
		if n > 0 {
			return peer, true, errPeerExited
		}
		peer.pidfd = true
	}
	return peer, false, nil
}
