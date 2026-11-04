//go:build !linux

package files

// linkLoopTries is 1 off Linux, so an ELOOP is not tried again. No row
// measured a retry on macOS or Windows.
const linkLoopTries = 1
