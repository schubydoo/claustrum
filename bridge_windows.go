//go:build windows

package main

import (
	"io"
	"os"
)

// bridgeStdout is the file itself on Windows, which has no SIGPIPE and keeps the
// copy as it was.
func bridgeStdout(f *os.File) io.Writer {
	return f
}
