//go:build windows

package supervisor

import (
	"errors"

	"golang.org/x/sys/windows"
)

// cwdUnusable is false on Windows. The second cwd test is not measured there
// (claustrum's own).
func cwdUnusable(string) bool { return false }

// startBusy tells if the start failed because a writer holds the program file
// open. Measured on Windows for a file that a writer holds open.
func startBusy(err error) bool { return errors.Is(err, windows.ERROR_SHARING_VIOLATION) }

// startNotExecutable tells if the program file is not a valid Win32 program.
// Measured on Windows for a text file.
func startNotExecutable(err error) bool { return errors.Is(err, windows.ERROR_BAD_EXE_FORMAT) }
