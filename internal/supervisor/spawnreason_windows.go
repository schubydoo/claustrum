//go:build windows

package supervisor

import (
	"errors"

	"golang.org/x/sys/windows"
)

// cwdUnusable is false on Windows. The second cwd test is not measured there
// (claustrum's own).
func cwdUnusable(string) bool { return false }

// startErrnoReason gives not_executable if the program file is not a valid Win32
// program (measured for a text file). It gives busy if a writer holds the program
// file open (measured for such a file). Else it gives "".
func startErrnoReason(err error) string {
	switch {
	case errors.Is(err, windows.ERROR_BAD_EXE_FORMAT):
		return "not_executable"
	case errors.Is(err, windows.ERROR_SHARING_VIOLATION):
		return "busy"
	}
	return ""
}
