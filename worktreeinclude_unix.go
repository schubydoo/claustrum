//go:build !windows

package main

// gitArgvBudget is the argv budget of one batched `git ls-files` call on Linux
// and macOS. The `.worktreeinclude` scan and the `.claude/` pass both use it.
// The value is a fit to the f6010b97 batch counts. It is
// not read from the reference. The `.claude/` pass pins 131 072 on Linux to
// the byte. The directory batches fit every value from 131 070 to 131 073 on
// Linux and macOS. The `.claude/` pass was not measured on macOS. The Windows
// budget is smaller. See worktreeinclude_windows.go.
const gitArgvBudget = 131072
