package main

import "os"

// stagedEntryModes does nothing on Windows: the staged files keep the modes
// of their create (copyRegistrationTree).
func stagedEntryModes(_, _ *os.Root, _ string) {}
