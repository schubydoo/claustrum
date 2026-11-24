//go:build windows

package git

import (
	"errors"

	"golang.org/x/sys/windows"
)

// linkTarget reads the reparse data of the entry at path itself, with
// FILE_FLAG_OPEN_REPARSE_POINT, and returns its substitute name
// (reparseSubstituteName). The open asks for no access right and shares every
// access, so it blocks no other user of the entry.
func linkTarget(path string) (string, bool) {
	data, ok := linkReparseData(path)
	if !ok {
		return "", false
	}
	return reparseSubstituteName(data)
}

// linkRelative reports the relative flag of the symbolic link at path.
func linkRelative(path string) bool {
	data, ok := linkReparseData(path)
	return ok && reparseIsRelative(data)
}

// linkReparseData reads the reparse data of the entry at path.
func linkReparseData(path string) ([]byte, bool) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, false
	}
	h, err := windows.CreateFile(p, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, false
	}
	defer func() { _ = windows.CloseHandle(h) }()
	buf := make([]byte, windows.MAXIMUM_REPARSE_DATA_BUFFER_SIZE)
	var n uint32
	if err := windows.DeviceIoControl(h, windows.FSCTL_GET_REPARSE_POINT, nil, 0, &buf[0], uint32(len(buf)), &n, nil); err != nil {
		return nil, false
	}
	return buf[:n], true
}

// networkAccessDenied reports whether err is ERROR_NETWORK_ACCESS_DENIED. Go
// reads ERROR_ACCESS_DENIED as fs.ErrPermission, but not this code. Which of the
// two a share that denies every account gives is not measured.
func networkAccessDenied(err error) bool {
	return errors.Is(err, windows.ERROR_NETWORK_ACCESS_DENIED)
}
