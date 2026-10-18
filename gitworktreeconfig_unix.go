//go:build unix

package main

import (
	"os"
	"time"
)

// writeConfigFileIn writes text as the new file `config.worktree` of entry,
// the root at the registration, on Linux and macOS. An entry at that name goes
// first, with one plain remove, never as a tree. 5fd08069 replaces a file and
// an empty folder there. Those are cells C4 and C5 of round task007-01 (Linux
// and macOS VMs). A folder with content fails the remove with `removeat config.worktree:
// directory not empty` (cell C6). Then the file is made with O_EXCL. The write
// time of the file of the daemon is not used here.
func writeConfigFileIn(entry *os.Root, text []byte, _ time.Time) error {
	if _, err := entry.Lstat(worktreeConfigName); err == nil {
		if err := entry.Remove(worktreeConfigName); err != nil {
			return err
		}
	}
	return writeRootFile(entry, worktreeConfigName, text, 0o666)
}
