//go:build !linux && !windows

package main

import "syscall"

// diskFullErrors are the error numbers that start the prune after a full disk.
// be1c1ca5 answers "no space left on device" on macOS (measured), the text of
// ENOSPC. EDQUOT is not measured on macOS, so it is not in the list.
var diskFullErrors = []error{syscall.ENOSPC}
