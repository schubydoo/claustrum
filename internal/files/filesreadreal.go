package files

import (
	"runtime"

	"github.com/schubydoo/claustrum/internal/logging"
)

// The two start lines of the files handler, as the rows quote them (build
// 683cc6a9, Linux). The level of each line is claustrum's own, not measured.
const (
	filesRealPassLine = "[FilesHandler] files.realpath: using the OS's path for the open handle"
	filesRealFailLine = "[FilesHandler] files.realpath: cannot read /proc/self/fd or mnt_id in /proc/self/fdinfo; falling back to filepath.EvalSymlinks, checked against the opened file; files.read will not return real"
)

// readRealStartLine gives the start line of the files handler on the system
// goos, or "" for none. Linux gives the line of its start test (683cc6a9).
// Windows gives the pass line, with no files.realpath call (4c111e86 and
// c9125eb8, measured on Windows). macOS gives the pass line too (4c111e86 and
// 87375cc7, measured on macOS).
func readRealStartLine(goos string, available func() bool) string {
	switch goos {
	case "linux":
		if available() {
			return filesRealPassLine
		}
		return filesRealFailLine
	case "darwin", "windows":
		return filesRealPassLine
	}
	return ""
}

// LogReadRealStart writes the start line of this system, before the first connection line.
func LogReadRealStart() {
	if line := readRealStartLine(runtime.GOOS, ReadRealAvailable); line != "" {
		logging.Infof("%s", line)
	}
}
