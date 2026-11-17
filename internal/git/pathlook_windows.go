//go:build windows

package git

import (
	"errors"

	"golang.org/x/sys/windows"
)

// lookCannotAccess reports whether err is ERROR_CANT_ACCESS_FILE, the error of a
// look below a folder whose reparse tag no filter handles (row w_new_t441).
var lookCannotAccess = func(err error) bool {
	return errors.Is(err, windows.ERROR_CANT_ACCESS_FILE)
}
