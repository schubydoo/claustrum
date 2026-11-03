//go:build linux

package procinfo

import (
	"os"
	"strings"
	"syscall"
)

// The linux process facts that the child records, the orphan reap, the host cleaner and
// the run-dir lock use: the /proc mount, the node identity and the pidfd hold.

// procRoot is the /proc mount that realReadLiveProc (package main) reads. A var so a test can point it at a
// fake procfs tree and exercise the parse-error arms.
var procRoot = "/proc"

// readBootID and osReadlink are seams over the /proc reads of NodeID below and of
// pidNamespaceRefusal (daemon_runlock_linux.go, package main). On the hosts
// this suite runs on the boot id is readable and non-empty and the pid-namespace
// link resolves; a procfs mounted subset=pid (systemd ProcSubset=) hides the boot
// id — the case the "" arms exist for — but no fixture can remount /proc, so
// NodeID's three "" arms and pidNamespaceRefusal's unreadable-self arm are
// unreachable without stand-ins.
var (
	readBootID = func() ([]byte, error) { return os.ReadFile("/proc/sys/kernel/random/boot_id") }
	osReadlink = os.Readlink
)

// NodeID builds the machine-plus-pid-namespace identity the reference writes into the
// owner record: the boot id joined to the pid-namespace
// inode with a single "/". It returns "" when either part is unavailable, and an empty
// node makes the signal guard refuse eviction.
func NodeID() string {
	boot, err := readBootID()
	if err != nil {
		return ""
	}
	b := strings.TrimSpace(string(boot))
	if b == "" {
		return ""
	}
	ns, err := osReadlink("/proc/self/ns/pid")
	if err != nil {
		return ""
	}
	return b + "/" + ns
}

// The two pidfd system calls. The syscall package holds no constant for them. The numbers
// are the same on every linux architecture that claustrum builds for (amd64 and arm64).
const (
	sysPidfdSendSignal = 424
	sysPidfdOpen       = 434
)

// hcPidfdOpen, hcPidfdSend and hcKillPid are the three system calls of HoldPID behind
// seams. A test can then see which one carried the signal, and it can stage a kernel
// without pidfd.
var (
	hcPidfdOpen = func(pid int) (int, error) {
		fd, _, errno := syscall.Syscall(sysPidfdOpen, uintptr(pid), 0, 0)
		if errno != 0 {
			return -1, errno
		}
		return int(fd), nil
	}
	hcPidfdSend = func(fd int, sig syscall.Signal) error {
		if _, _, errno := syscall.Syscall6(sysPidfdSendSignal, uintptr(fd), uintptr(sig), 0, 0, 0, 0); errno != 0 {
			return errno
		}
		return nil
	}
	hcKillPid = syscall.Kill
)

// HoldPID opens a pid file descriptor for the single process pid (pidfd_open). The
// send that it returns signals through that descriptor (pidfd_send_signal), and release
// closes it. The reference retires a daemon with pidfd_send_signal(<fd>, SIGTERM, NULL, 0).
// A Linux test measured that with strace against f6010b97 and 89cb6289. The host cleaner checks
// the identity of pid between the open and the send. The serve eviction of the run-dir lock
// holder does the same (holdHolder, package main). -stop (realSignalHolder) does not: it checks before
// the open only.
//
// If the kernel has no such call (ENOSYS), the signal goes out with kill(pid, sig). That
// fallback is claustrum's own choice. The reference on such a kernel is not measured. Any
// other error of the open comes back from send, and no signal goes out.
func HoldPID(pid int) (send func(syscall.Signal) error, release func()) {
	fd, err := hcPidfdOpen(pid)
	if err == syscall.ENOSYS {
		return func(sig syscall.Signal) error { return hcKillPid(pid, sig) }, func() {}
	}
	if err != nil {
		return func(syscall.Signal) error { return err }, func() {}
	}
	send = func(sig syscall.Signal) error {
		if err := hcPidfdSend(fd, sig); err != syscall.ENOSYS {
			return err
		}
		return hcKillPid(pid, sig)
	}
	return send, func() { _ = syscall.Close(fd) }
}
