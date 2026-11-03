//go:build linux || darwin

package hostsweep

import (
	"net"
	"syscall"
	"time"

	"github.com/schubydoo/claustrum/internal/procinfo"
	"github.com/schubydoo/claustrum/internal/runlock"
	"github.com/schubydoo/claustrum/internal/sockroute"
)

// The names of internal/procinfo, internal/runlock and internal/sockroute under the
// names that the code of this package uses.

const runDirLockName = runlock.LockName

type ownerRecord = runlock.OwnerRecord

func killGroup(pid int, sig syscall.Signal) error { return procinfo.KillGroup(pid, sig) }

func hcHoldDaemon(pid int) (send func(syscall.Signal) error, release func()) {
	return procinfo.HoldPID(pid)
}

func dialSocketInDaemon(path string, timeout time.Duration) (net.Conn, error) {
	return sockroute.DialSocketInDaemon(path, timeout)
}
