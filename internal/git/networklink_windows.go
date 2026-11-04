//go:build windows

package git

import "golang.org/x/sys/windows"

// linkTarget reads the reparse data of the entry at path itself, with
// FILE_FLAG_OPEN_REPARSE_POINT, and returns its substitute name
// (reparseSubstituteName). The open asks for no access right and shares every
// access, so it blocks no other user of the entry.
func linkTarget(path string) (string, bool) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", false
	}
	h, err := windows.CreateFile(p, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", false
	}
	defer func() { _ = windows.CloseHandle(h) }()
	buf := make([]byte, windows.MAXIMUM_REPARSE_DATA_BUFFER_SIZE)
	var n uint32
	if err := windows.DeviceIoControl(h, windows.FSCTL_GET_REPARSE_POINT, nil, 0, &buf[0], uint32(len(buf)), &n, nil); err != nil {
		return "", false
	}
	return reparseSubstituteName(buf[:n])
}
