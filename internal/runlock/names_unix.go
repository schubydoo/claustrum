//go:build linux || darwin

package runlock

import (
	"syscall"

	"github.com/schubydoo/claustrum/internal/procinfo"
	"github.com/schubydoo/claustrum/internal/tokenfile"
)

// The names of internal/procinfo and internal/tokenfile under the names that the code
// of this package uses.

const persistedTokenName = tokenfile.Name

func tokenFileDir(socket string) string { return tokenfile.Dir(socket) }

func nodeID() string { return procinfo.NodeID() }

func hcHoldDaemon(pid int) (send func(syscall.Signal) error, release func()) {
	return procinfo.HoldPID(pid)
}
