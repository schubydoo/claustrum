package main

import "syscall"

// diskFullErrors are the error numbers that start the prune after a full disk.
// be1c1ca5 answers "no space left on device", the text of ENOSPC. A user
// quota answers "disk quota exceeded", the text of EDQUOT (both measured on
// Linux).
var diskFullErrors = []error{syscall.ENOSPC, syscall.EDQUOT}
