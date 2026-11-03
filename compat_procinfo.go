//go:build linux || darwin

package main

import (
	"syscall"

	"github.com/schubydoo/claustrum/internal/procinfo"
)

// The names of internal/procinfo under the names that the code of package main
// uses.

func killGroup(pid int, sig syscall.Signal) error { return procinfo.KillGroup(pid, sig) }

func nodeID() string { return procinfo.NodeID() }
