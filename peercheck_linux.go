//go:build linux

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"slices"
	"strconv"

	"golang.org/x/sys/unix"
)

// How the parts of the own text are read is claustrum's own, not measured. The
// user part is the link /proc/self/ns/user. The net part is SO_NETNS_COOKIE of a
// unix socket. The pidfd part is there when SO_PEERPIDFD works on a unix socket.

// The reads of the check. A test replaces one to give it an errno.
var (
	peerReadlink = os.Readlink
	peerNSNames  = nsDirNames
	peerCookieOf = func(fd int) (uint64, error) {
		return unix.GetsockoptUint64(fd, unix.SOL_SOCKET, unix.SO_NETNS_COOKIE)
	}
	peerCredOf = func(fd int) (*unix.Ucred, error) {
		return unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	}
)

// nsDirNames lists the names in the folder dir, as /proc/<pid>/ns.
func nsDirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}

// errNSListsUser is the error of a user link that is gone while its folder lists
// it. The text is the one of 4c111e86 (measured on Linux).
func errNSListsUser(dir string) error {
	return fmt.Errorf("%s lists %q, which cannot be read: %w", dir, "user", unix.ENOMEM)
}

// daemonNSText reads the own text of the daemon. A part that the kernel does not
// give stays empty. A failed read of the user link is an ownNamingError, as on
// 4c111e86 (measured on Linux). A link that is gone gives no user part, unless
// /proc/self/ns still lists "user" (measured). A failed list gives no user part
// (claustrum's own, not measured).
func daemonNSText() (nsText, error) {
	var t nsText
	link, err := peerReadlink("/proc/self/ns/user")
	switch {
	case err == nil:
		t.user = link
	case errors.Is(err, fs.ErrNotExist):
		if names, lerr := peerNSNames("/proc/self/ns"); lerr == nil && slices.Contains(names, "user") {
			return nsText{}, ownNamingError{errNSListsUser("/proc/self/ns")}
		}
	default:
		return nsText{}, ownNamingError{err}
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return t, nil
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
	return t, nil
}

// ownUserNow reads the user link of the daemon again, at one connection. It
// reads /proc/self first, as 4c111e86 does (measured on Linux). A link that is
// gone gives the empty user part (measured). Another failure is an
// ownNamingError (measured for ENFILE, ENOMEM and EACCES). The value of
// /proc/self is not used (claustrum's own, not measured).
func ownUserNow() (string, error) {
	for _, path := range []string{"/proc/self", "/proc/self/ns/user"} {
		link, err := peerReadlink(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return "", nil
		case err != nil:
			return "", ownNamingError{err}
		case path == "/proc/self/ns/user":
			return link, nil
		}
	}
	return "", nil
}

// lookupPeer builds the own text of the process at the other end of nc, with the
// parts that own has. The steps follow the order of 4c111e86 (measured on Linux):
// the cookie of nc, SO_PEERCRED, SO_PEERPIDFD, the own user link again, and then
// the link /proc/<pid>/ns/user. The pidfd must not be readable yet, after the
// other reads. exited is true for a caller that is gone: a readable pidfd or no
// namespace folder (claustrum's own, not measured).
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

// peerPidfdOf reads SO_PEERPIDFD of the socket fd. A test replaces it to give
// the read an errno.
var peerPidfdOf = func(fd int) (int, error) {
	return unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PEERPIDFD)
}

// The texts of a failed SO_NETNS_COOKIE or SO_PEERCRED read are the bare errno
// text, as on 4c111e86 (measured on Linux for ENFILE and ENOMEM).
func lookupPeerFD(fd int, own nsText) (peer nsText, exited bool, err error) {
	if own.net != "" {
		cookie, err := peerCookieOf(fd)
		if err != nil {
			return peer, false, err
		}
		peer.net = "net:" + strconv.FormatUint(cookie, 10)
	}
	cred, err := peerCredOf(fd)
	if err != nil {
		return peer, false, err
	}
	if cred.Pid <= 0 {
		return peer, false, errors.New("the caller has no pid in this namespace")
	}
	pfd := -1
	if own.pidfd {
		// A Linux 6.12 host answers EINVAL for a caller that is gone (no test
		// pins the errno). ESRCH counts as gone too (claustrum's own, not measured).
		// For EMFILE the text is "caller <pid>: too many open files" (measured on
		// Linux, c9125eb8 and 4c111e86). ENFILE and ENOMEM have the same form
		// (measured on Linux, 4c111e86).
		pfd, err = peerPidfdOf(fd)
		if err != nil {
			gone := errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ESRCH)
			return peer, gone, fmt.Errorf("caller %d: %w", cred.Pid, err)
		}
		defer unix.Close(pfd)
	}
	if own.user != "" {
		user, err := ownUserNow()
		if err != nil {
			return peer, false, err
		}
		if user != "" {
			link, exited, err := peerUserLink(cred.Pid)
			if err != nil {
				return peer, exited, err
			}
			peer.user = link
		}
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

// peerUserLink reads the link /proc/<pid>/ns/user of a caller, on a daemon that
// has a user part. A link that is gone sends the check to the folder
// /proc/<pid>/ns. With "user" listed, or with no "user" listed, the error has
// the text of 4c111e86 (measured on Linux). A folder that is gone tells an
// exited caller (claustrum's own, not measured).
func peerUserLink(pid int32) (link string, exited bool, err error) {
	link, err = peerReadlink(fmt.Sprintf("/proc/%d/ns/user", pid))
	if !errors.Is(err, fs.ErrNotExist) {
		return link, false, err
	}
	dir := fmt.Sprintf("/proc/%d/ns", pid)
	names, lerr := peerNSNames(dir)
	switch {
	case errors.Is(lerr, fs.ErrNotExist):
		return "", true, err
	case lerr != nil:
		return "", false, lerr
	case slices.Contains(names, "user"):
		return "", false, errNSListsUser(dir)
	}
	return "", false, fmt.Errorf("%s shows no user namespace, on a system that has them: %w", dir, unix.ENOMEM)
}
