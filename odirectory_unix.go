//go:build unix

package main

import "syscall"

// oDirectoryFlag makes os.OpenFile in filesList fail with ENOTDIR the moment a
// non-directory is opened, rather than opening it and failing later at the
// readdir. 7d193f89's files.list opens with O_DIRECTORY, so on the wire a regular
// file (readable or not) reports `open <p>: not a directory` and an unreadable
// directory reports `open <p>: permission denied` — both matched byte-for-byte.
// Before this the reference (and claustrum) reached the readdir and reported
// `readdirent <p>: not a directory`.
const oDirectoryFlag = syscall.O_DIRECTORY
