//go:build unix

package supervisor

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

// cwdUnusable tells if a cwd that exists cannot be entered. It tests the search
// permission of the folder. Measured on Linux and macOS for a folder of mode 000.
func cwdUnusable(cwd string) bool {
	return unix.Access(cwd, unix.X_OK) != nil
}

// startBusy tells if the start failed because the program file is open for a write.
// Linux measured ETXTBSY for an ELF file and a script. On macOS no row shows this.
func startBusy(err error) bool { return errors.Is(err, syscall.ETXTBSY) }

// startNotExecutable is false on Linux and macOS. The Linux not_executable rows
// reach the permission test of ReasonForSpawnError.
func startNotExecutable(error) bool { return false }
