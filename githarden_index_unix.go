//go:build unix

package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// installWorktreeIndex puts the index that the checkout wrote at src into adminDir,
// the registration of the new worktree, as the file "index". It makes a new file
// there and copies the bytes, so the file equals the one of 89cb6289 in these facts
// (Linux and macOS VMs, rows A1 to A13):
//
//   - It is a new inode, not the temporary file moved.
//   - Its group is the group that a new file gets in the registration (rows A1, A3,
//     A5, A6, A8 and A9).
//   - Its mode is 0666 less the umask of the daemon (row A12). It follows neither the
//     mode of the temporary file nor core.sharedRepository (row A13).
//   - Its mtime is the mtime of the temporary file, rounded up to a whole
//     microsecond. The atime is not set: no row measures it.
//
// A temporary index that is gone is a failed placement. The error reads "open
// <src>: no such file or directory", the text of cell X1 (Linux and macOS VMs).
//
// The file is made through a root at the parent of adminDir, the registrations
// directory, so each error names "<name>/index". The order is claustrum's own, and
// it fits every measured row of 89cb6289:
//
//  1. Create the file, exclusively. Any error but "file exists" is the answer:
//     "openat <name>/index: permission denied" for a registration of mode 0500
//     (rows A14, A14f and A14b) and for a registrations directory of mode 0600,
//     with or without a file at the index (cells Z11a and Z11b, macOS VM), and
//     "openat <name>/index: no such file or directory" for a registration that is
//     gone (cell Z10, macOS VM).
//  2. On "file exists", remove the entry with one plain remove, never a tree. A
//     file, a symlink and an empty folder go (cells X8, Z7b and Z7a). The target of
//     the symlink stays. A failed remove is the answer: "removeat <name>/index:
//     permission denied" in a registration of mode 0500 (cell Y3), and "removeat
//     <name>/index: directory not empty" for a folder that holds a file (cell Y7).
//  3. Create the file again, exclusively. An entry that another process made in
//     between gives "openat <name>/index: file exists" (not measured). The open
//     does not follow a link there.
//
// A failure after the create leaves the file as it is, and the rollback of the
// caller deletes the registration.
func installWorktreeIndex(src, adminDir string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	// git writes a clean path, and that one is used as it is. A path that is not
	// clean is resolved first: a lexical clean drops a "link/.." pair and can name
	// another folder than the kernel does.
	if adminDir != filepath.Clean(adminDir) {
		adminDir, err = filepath.EvalSymlinks(adminDir)
		if err != nil {
			return err
		}
	}
	root, err := os.OpenRoot(filepath.Dir(adminDir))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	name := filepath.Base(adminDir) + "/index"
	create := func() (*os.File, error) {
		return root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	}
	out, err := create()
	if errors.Is(err, fs.ErrExist) {
		if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		out, err = create()
	}
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return root.Chtimes(name, time.Time{}, roundUpToMicrosecond(st.ModTime()))
}

// removeCreatedRegistration deletes the registration that createdWorktreeAdminDir
// verified. It is step B of undoFailedCheckout. The delete acts on the resolved path
// that the check compared, never on a lexical clean of the raw path: a clean drops a
// "link/.." pair and can name another folder than the kernel does. The root is the
// resolved registrations directory, so the root is the containment from the open
// on, and the delete never follows a symlink out of it. The open itself follows a
// symlink that replaced the directory after the check. So the opened directory
// must have the identity that the check recorded (os.SameFile). If it differs,
// the delete is refused. The error of a failed delete reads "RemoveAll <name>: <OS
// error>", the wording of row A14.
//
// A registration that is not a direct child of the registrations directory is
// refused, and nothing is deleted. git writes each registration as a direct child.
// The zero value and a registrations directory that does not exist have nothing to
// delete.
//
// A registration whose back-pointer the check cannot read for a permission
// error is not deleted either, and that error is returned. The rollback then
// keeps the branch and names the registration in the frame, as 89cb6289 does with
// a registrations directory of mode 0600 (cells Z11a and Z11b, macOS VM). The error
// reads "RemoveAll <name>: permission denied", as in that frame. No delete is
// attempted: claustrum does not delete what it did not verify.
func removeCreatedRegistration(reg createdRegistration) error {
	if reg.unreadable != nil {
		return reg.unreadable
	}
	if reg.resolved == "" {
		return nil
	}
	name := filepath.Base(reg.resolved)
	if reg.resolved != filepath.Join(reg.registry, name) {
		return fmt.Errorf("%s is not a direct child of %s", reg.resolved, reg.registry)
	}
	root, err := os.OpenRoot(reg.registry)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer func() { _ = root.Close() }()
	if now, err := root.Stat("."); err != nil || !os.SameFile(reg.registryInfo, now) {
		return fmt.Errorf("%s is no longer the directory that was checked", reg.registry)
	}
	return root.RemoveAll(name)
}

// roundUpToMicrosecond returns t when it is a whole microsecond, and the next whole
// microsecond otherwise. No row holds a temporary index with a whole microsecond
// mtime, so the first case is claustrum's choice (not measured).
func roundUpToMicrosecond(t time.Time) time.Time {
	if whole := t.Truncate(time.Microsecond); !whole.Equal(t) {
		return whole.Add(time.Microsecond)
	}
	return t
}
