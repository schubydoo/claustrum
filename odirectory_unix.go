//go:build unix

package main

import "syscall"

// oDirectoryFlag makes os.OpenFile in filesList fail with ENOTDIR the moment a
// non-directory is opened, rather than opening it and failing later at the
// readdir. On the wire, 7d193f89's files.list reports `open <p>: not a directory`
// for a regular file, readable or not. An unreadable directory reports
// `open <p>: permission denied`. claustrum matches both byte for byte. Before
// 7d193f89, the reference and claustrum both reported
// `readdirent <p>: not a directory`.
const oDirectoryFlag = syscall.O_DIRECTORY
