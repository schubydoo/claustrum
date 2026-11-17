//go:build windows

package git

import (
	"errors"

	"golang.org/x/sys/windows"
)

// lookCannotAccess reports whether err is ERROR_CANT_ACCESS_FILE, the error of a
// look below a folder whose reparse tag no filter handles (4c111e86, measured
// on Windows).
var lookCannotAccess = func(err error) bool {
	return errors.Is(err, windows.ERROR_CANT_ACCESS_FILE)
}
