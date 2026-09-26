package main

// gitArgvBudget is the argv budget of one batched `git ls-files` call on
// Windows. The `.worktreeinclude` scan and the `.claude/` pass both use it.
// The value is a fit to the f6010b97 batch counts, found by bisection. It is
// not read from the reference. A one-byte bisection pins it in both passes. The
// Linux and macOS budget is larger. See worktreeinclude_unix.go.
const gitArgvBudget = 24576
