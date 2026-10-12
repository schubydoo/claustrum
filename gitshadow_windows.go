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

// shadowStagesAdd is true on Windows. The `worktree add` of a create gets a
// folder `.s<n>` of its own there in the state shadow, and the registration
// goes into the git directory by a staging folder and a rename
// (worktreeAddStaged). 5fd08069 does so on a Windows VM (cells 44 of
// s3p2b-edges2-windows and 47 to 49 of s3p2b-edges3-windows).
const shadowStagesAdd = true

// gitDirMustBeReal is true on Windows. A git directory that is a link or a
// junction is refused there, and a create tests at each of its steps that the
// git directory is still the folder that its gate accepted (acceptedGitDir).
// 5fd08069 refuses so on a Windows VM (cells X1c and X1p of
// val-s3p2b2-7-windows). It is a variable so that a test on another system can
// run those steps.
var gitDirMustBeReal = true

// statusFollowsPrintedGitDir is true on Windows. The trust check resolves no
// junction there, so git can print another spelling of the git directory of a
// request. A git.status then goes on with the printed path (usePrintedGitDir,
// statusPrintedRootSpelling). 5fd08069 does so on a Windows VM (cell D6j-junction
// of val-s3p2b2-3-windows).
const statusFollowsPrintedGitDir = true

// openShadowLockFile makes the lock file of the daemon (0 bytes) and opens it
// with no sharing. The open handle is the hold. A Windows VM measured the lock
// file of a live 5fd08069 daemon. Every open with data access fails there, with
// every share mode. An open with delete access fails too. An open for
// attributes works (val-s3p2b2-1-windows row D2). The file cannot be removed
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
