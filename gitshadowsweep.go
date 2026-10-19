package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The sweep of the folder of all daemons, <socket folder>/git-shadow. A daemon
// that makes its own folder there removes the folders of daemons that are gone.
// 5fd08069 does so in round task075-01 (cells G1 to G12, Linux VM). Cells G1,
// G3 to G10 and G12 give the same outcomes on a macOS VM.
//
// Every name of the sweep comes from a directory read of git-shadow, never from
// a request. A folder goes through removeEntry and a link through removeLink.
// No step follows a link out of git-shadow to write: a planted link gets no lock
// file in its target (D8).

// shadowSweepAge is the age past which the sweep removes an entry whose name does
// not start with the host form of this host. On 5fd08069 an entry 8 days old
// went and one 6 days old stayed (cells G7b and G7c of round task075-01, Linux
// VM). The bound of 7 days is not measured.
const shadowSweepAge = 7 * 24 * time.Hour

// errSweepLockKind is the refusal of a lock entry that is no regular file.
var errSweepLockKind = errors.New("the lock entry is not a regular file")

// sweepOldFolders removes from git-shadow each folder whose lock this daemon
// can take. own is the name of the folder of this daemon, which the sweep
// skips. The caller holds mu, right after makeFolder made own.
//
// A folder whose name has the host form of this host goes as a tree (cells G1,
// G4, G6 and G12). A folder with another name goes only when its mtime is more
// than shadowSweepAge old. On Linux and macOS the age is read after the lock
// step, so a new lock file makes the folder new (cells G7, G7b and G7c). On
// Windows it is read before (sweepAgeBeforeLock). A folder whose lock is
// held stays (cells G2 and G3). An entry whose lock cannot be opened stays: a
// folder of mode 000, a regular file and a lock that is a link (cells G5, G8
// and G9). A remove that fails part way leaves the rest, and the request goes
// on (cell G6). With git-shadow a link to a folder, the sweep works in that
// folder (cell G11).
func (h *shadowHost) sweepOldFolders(own string) {
	base := filepath.Join(h.runDir, shadowRootName)
	root, err := os.OpenRoot(base)
	if err != nil {
		return
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(".")
	if err != nil {
		return
	}
	names, _ := f.Readdirnames(-1)
	_ = f.Close()
	slices.Sort(names)
	prefix := hostForm() + "-"
	for _, name := range names {
		if name != own {
			h.sweepEntry(root, base, name, prefix)
		}
	}
}

// sweepEntry is the step of sweepOldFolders on one entry of git-shadow. root is
// a root at base, the folder git-shadow.
func (h *shadowHost) sweepEntry(root *os.Root, base, name, prefix string) {
	fi, err := root.Lstat(name)
	if err != nil {
		return
	}
	if isSweepLink(fi) {
		h.sweepLink(base, name, prefix)
		return
	}
	if !fi.IsDir() {
		return
	}
	rel := filepath.Join(name, shadowLockName)
	if err := sweepLockKind(root, rel); err != nil {
		return
	}
	lock, err := takeSweepLock(root, base, rel)
	if err != nil {
		return
	}
	if !strings.HasPrefix(name, prefix) {
		if !sweepAgeBeforeLock {
			fi, err = root.Lstat(name)
		}
		if err != nil || time.Since(fi.ModTime()) <= shadowSweepAge {
			_ = lock.Close()
			return
		}
	}
	if sweepClosesLockFirst {
		_ = lock.Close()
		_ = h.removeEntry(name)
		return
	}
	_ = h.removeEntry(name)
	_ = lock.Close()
}

// sweepLockKind refuses a lock entry at rel that is there and is no regular
// file, or whose look fails. A missing entry passes: the lock step makes it.
// On 5fd08069 the open of a lock that is a link fails with ELOOP, and the
// folder stays (cell G9). Another kind of entry is not measured.
func sweepLockKind(root *os.Root, rel string) error {
	fi, err := root.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return errSweepLockKind
	}
	return nil
}

// sweepLink is the step of sweepOldFolders on a symbolic link of git-shadow.
//
// 5fd08069 takes the lock through the link, makes an empty lock file in the
// target and removes the link. The content of the target stays (cell G10 of
// round task075-01, Linux VM). claustrum makes no file in the target (D8). It
// tests the lock of the target with a read open, which makes nothing. A lock
// that is held keeps the link (not measured). Then the link goes with one plain
// remove (removeLink). On a Windows VM 5fd08069 does the same for a directory
// symbolic link and for a junction, and it makes the lock file in the target
// too (cells G10 and G10j of round task075-01). The age of a link with another
// name is the mtime of its target (not measured). A link to a file or to
// nothing stays (not measured).
func (h *shadowHost) sweepLink(base, name, prefix string) {
	path := filepath.Join(base, name)
	fi, err := os.Stat(path)
	if err != nil || !fi.IsDir() {
		return
	}
	if linkedLockHeld(filepath.Join(path, shadowLockName)) {
		return
	}
	if !strings.HasPrefix(name, prefix) && time.Since(fi.ModTime()) <= shadowSweepAge {
		return
	}
	_ = h.removeLink(name)
}

// linkedLockHeld reports whether the lock file at path, in the target of a
// link, is held or cannot be tested. A missing lock file is not held. The test
// writes nothing (linkedLockOpen).
func linkedLockHeld(path string) bool {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil || !fi.Mode().IsRegular() {
		return true
	}
	return linkedLockOpen(path) != nil
}
