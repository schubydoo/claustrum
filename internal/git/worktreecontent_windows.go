package git

import "io/fs"

// lacksExecBit is false on Windows: a file there has no execute bit, and the
// check has no such test (claustrum's own, not measured).
func lacksExecBit(fs.FileInfo) bool { return false }

// committedLinkFault finds no fault on Windows: the chain test of a committed
// link is measured on Linux and macOS only, and Windows keeps the check
// without it (claustrum's own, not measured).
func committedLinkFault(string, string) linkFault { return linkNoFault }
