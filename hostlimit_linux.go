//go:build linux

package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// hostLimitPath is the host settings file (13047a51, measured on Linux). A test points it
// at a file of its own.
var hostLimitPath = "/etc/claude-desktop-ssh/settings.json"

// hostLimitFeatures lists server.unattended_limit right after server.instance_id on Linux,
// also with no file and with a file that cannot be used (13047a51, measured on Linux).
var hostLimitFeatures = []string{hostLimitFeature}

// limitEndRuns is true on Linux, where the daemon ends at the limit (limitend.go).
const limitEndRuns = true

// readHostLimitFile gives the bytes of the regular file at path, at most hostLimitMaxSize.
// The open does not wait for a FIFO (claustrum's own).
func readHostLimitFile(path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, unreadableFault(path, &fs.PathError{Op: "open", Path: path, Err: err})
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errHostLimitNotFile
	}
	if err := hostFileRules(path, fi); err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(f, hostLimitMaxSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > hostLimitMaxSize {
		return nil, errHostLimitTooLarge
	}
	return b, nil
}

// unreadableFault gives the fault of an open of path that failed with openErr. For a
// refused open of a regular file the owner and mode rules come first: 13047a51 gives the
// write text for a root file of mode 0642 (measured on Linux). The folder rule in that case
// is claustrum's own, not measured. Any other failure gives openErr.
func unreadableFault(path string, openErr error) error {
	if !errors.Is(openErr, fs.ErrPermission) {
		return openErr
	}
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return openErr
	}
	var ff *hostFileFault
	if errors.As(hostFileRules(path, fi), &ff) {
		return ff
	}
	return openErr
}

// hostLimitOwnUID is the account of the daemon. A test gives it another value.
var hostLimitOwnUID = os.Getuid

// hostLimitWriteBits are the group and other write bits. 0664, 0666 and 0646 on the file
// and 0775, 0777, 0757 and 0753 on its folder break the rule (13047a51, measured on Linux).
// Other modes with one of these bits are claustrum's own, not measured.
const hostLimitWriteBits = 0o022

// hostFileRules tests the owner and mode of the opened file fi at path and the mode of
// its folder (13047a51, measured on Linux). A file of the daemon account breaks the owner
// rule. A root file passes. The order of the three tests is claustrum's own. Claustrum's
// own, not measured: a file of another account passes, the tests read the target of a
// link, and only the folder that names the file is tested, with no owner test and no
// exception for a sticky bit.
func hostFileRules(path string, fi fs.FileInfo) error {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid != 0 && int(st.Uid) == hostLimitOwnUID() {
		return &hostFileFault{hostFaultOwner, fmt.Sprintf("`%s` belongs to the account that signs in (uid %d). Root must own the settings file and every folder above it.", path, st.Uid)}
	}
	if m := fi.Mode().Perm(); m&hostLimitWriteBits != 0 {
		return &hostFileFault{hostFaultWritable, writableDetail(path, m)}
	}
	dir := filepath.Dir(path)
	di, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if m := di.Mode().Perm(); m&hostLimitWriteBits != 0 {
		return &hostFileFault{hostFaultWritable, writableDetail(dir, m)}
	}
	return nil
}

func writableDetail(path string, m fs.FileMode) string {
	return fmt.Sprintf("`%s` can be changed by accounts other than its owner (mode %04o). Remove that write access.", path, uint32(m))
}
