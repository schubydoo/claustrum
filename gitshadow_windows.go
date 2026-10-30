//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

// openHeldGitDir opens the git directory at path as the root that a create
// holds until it answers (acceptGitDir). The handle shares read, write and
// delete, so another process can rename the folder while the create runs.
//
// os.OpenRoot gives no such handle. From the code of Go 1.26.9, it opens the
// path with syscall.Open (os/root_windows.go, openRootNolog). That call has the
// share mode `FILE_SHARE_READ | FILE_SHARE_WRITE` (syscall/syscall_windows.go,
// Open). With that hold, a rename of `repo\.git` during a create failed on a
// Windows VM. The text was `The process cannot access the file because it is
// being used by another process`. The rename passed between two requests
// (section 1.1 and control H1 of val-s3p2b2-9-windows). On 5fd08069 the rename
// passes during a create, except after its calls 7 and 8 (that round).
//
// Root.OpenRoot opens a name inside a root with NtCreateFile and the share mode
// `FILE_SHARE_READ|FILE_SHARE_WRITE|FILE_SHARE_DELETE`
// (internal/syscall/windows/at_windows.go, Openat). So the folder is opened by
// its last name through a root of its parent folder. That root is closed
// before the return. The open follows no link at the last name: a junction
// there is an error. A parent folder that the daemon cannot open is not
// measured: the create is then refused as for a git directory that changed. A
// path with no parent folder, the root of a volume, is opened by the path.
// With this open the rename passes on claustrum at each swap point of a
// create (section 1.4 of val-s3p2b2-12-windows, Windows VM).
func openHeldGitDir(path string) (*os.Root, error) {
	clean := filepath.Clean(path)
	dir := filepath.Dir(clean)
	if dir == clean {
		return os.OpenRoot(path)
	}
	parent, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = parent.Close() }()
	return parent.OpenRoot(filepath.Base(clean))
}

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

// gitDirMustBeReal is true on Windows. A git directory that is a junction or
// a directory symbolic link is refused there, and a create tests at each of its steps that the
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

// probeConfigFreeRun is false on Windows: the call about the repository keeps
// the two runs of c8e2f083 there (secondProbe). The third run of Linux and
// macOS is not measured on Windows (claustrum's own, not measured).
const probeConfigFreeRun = false

// makeRequestDir makes the short-lived folder name of a request (mode 0700) in
// folder, the folder of the daemon (requestDir). On Windows it is a plain
// mkdir by its full path through requestDirMkdir, not through root. With a
// deny entry for new folders on that folder and the backup and restore
// privileges enabled, the create of 5fd08069 succeeds, and the root
// mkdir of claustrum fails (cells X3 of rounds task077-03 and task077-05,
// Windows VM). By path the create of claustrum succeeds there (round task077-06). With
// both privileges disabled both builds fail alike (cells X3c). Those cells
// reach .s<n> and .index-<n>. The folders .config-<n>, .worktreeinclude-<n>
// and the .gitdir-<n> of git.status are not measured (claustrum's own).
func makeRequestDir(_ *os.Root, folder, name string) error {
	return requestDirMkdir(filepath.Join(folder, name), 0o700)
}

// listedSwap is the test of a create after the first listing of the
// checkout. The path of the git directory must still name the held folder
// there. If not, changed is true and the create answers errGitDirChanged. A
// regular file at the path gives fileSwap instead. The create then runs the
// second listing before it answers errGitDirChanged, as 5fd08069 does (cell X2
// of round task077-03, Windows VM).
func (u gitUse) listedSwap() (fileSwap, changed bool) {
	if !u.swapped("listed") {
		return false, false
	}
	if fileAtGitDir(u.gitDir) {
		return true, false
	}
	return false, true
}

// fileAtGitDir reports whether the entry at gitDir is a regular file, not a
// junction, a symbolic link or a folder.
func fileAtGitDir(gitDir string) bool {
	fi, err := os.Lstat(gitDir)
	return err == nil && fi.Mode().IsRegular()
}

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

// shadowLockBusy reports whether err of openShadowLockFile is a lock that
// another holder has open (newOwnFolder).
func shadowLockBusy(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}

// shadowLockAt is true on Windows. A lock file that is open with no sharing
// cannot be removed there (s3-windows cell B6).
func shadowLockAt(*os.File, string) bool { return true }

// sweepAgeBeforeLock is true on Windows. On a Windows VM 5fd08069 removes a
// folder of another host form with no lock file at 8 days, so the age is read
// before the lock step makes that file (cell G7 of round task075-01).
const sweepAgeBeforeLock = true

// isSweepLink reports whether an entry of git-shadow is a link: a directory
// symbolic link or a junction. Go reads a junction as an irregular entry, so
// the reparse attribute of a folder counts too (cells G10 and G10j of round
// task075-01, Windows VM).
func isSweepLink(fi os.FileInfo) bool {
	if fi.Mode()&os.ModeSymlink != 0 {
		return true
	}
	d, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	const both = syscall.FILE_ATTRIBUTE_REPARSE_POINT | syscall.FILE_ATTRIBUTE_DIRECTORY
	return ok && fi.Mode()&os.ModeIrregular != 0 && d.FileAttributes&both == both
}

// sweepClosesLockFirst is true on Windows. An open file cannot be removed
// there, so the sweep closes the lock of a folder before it removes the folder
// (not measured).
const sweepClosesLockFirst = true

// takeSweepLock makes the lock file rel of a folder in git-shadow through root
// when it is not there. Then it opens the file at its path below base with no
// sharing, and that open is the lock. A lock that another process holds open
// with no sharing refuses both opens, and the folder then stays. A lock that
// opens is the lock of a daemon that is gone (cells G1 and G3 of round
// task075-01, Windows VM). The create goes through root, so it follows no link
// out of git-shadow. The second open makes nothing and follows no link at the
// last name.
func takeSweepLock(root *os.Root, base, rel string) (*os.File, error) {
	f, err := root.OpenFile(rel, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	_ = f.Close()
	return openNoShare(filepath.Join(base, rel), syscall.GENERIC_READ|syscall.GENERIC_WRITE)
}

// linkedLockOpen tests the lock file at path with an open with no sharing,
// and closes it at once. The open makes nothing (claustrum's own, not
// measured).
func linkedLockOpen(path string) error {
	f, err := openNoShare(path, syscall.GENERIC_READ)
	if err != nil {
		return err
	}
	return f.Close()
}

// openNoShare opens the file at path that is there, with access and no
// sharing. It follows no link at the last name.
func openNoShare(path string, access uint32) (*os.File, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	h, err := syscall.CreateFile(name, access, 0, nil, syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}
