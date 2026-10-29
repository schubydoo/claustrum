//go:build unix

package main

import (
	"io"
	"os"
)

// bridgeStdout returns the writer that the bridge copies the socket into. The
// wrapper hides the ReadFrom method of the file, so io.Copy makes plain writes
// and takes no splice path. A write to a closed stdout then fails with EPIPE.
func bridgeStdout(f *os.File) io.Writer {
	return struct{ io.Writer }{f}
}
