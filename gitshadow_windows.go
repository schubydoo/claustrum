//go:build windows

package main

import (
	"os"
	"syscall"
)

// shadowLinksWorktrees is false on Windows. The copy of a repository has no
// `worktrees` entry and no reparse point there on 5fd08069 (s3-windows cell A3
// and cell 22 of s3p2b-edges, Windows VM).
const shadowLinksWorktrees = false

// openShadowLockFile makes the lock file of the daemon (0 bytes) and opens it
// with no sharing. The open handle is the hold. On a Windows VM every open of
// the lock file of a live 5fd08069 daemon with data access fails, with every
// share mode, and so does an open with delete access. An open for attributes
// works (val-s3p2b2-1-windows row D2). The file cannot be removed
// (s3-windows cell B6). A handle that os.OpenFile makes shares read and write,
// so the open is made here.
func openShadowLockFile(path string) (*os.File, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil,
		syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}
