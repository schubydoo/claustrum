package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// The two start lines of the files handler, as the rows quote them (build
// 683cc6a9, Linux VM). The level of each line is claustrum's own, not measured.
const (
	filesRealPassLine = "[FilesHandler] files.realpath: using the OS's path for the open handle"
	filesRealFailLine = "[FilesHandler] files.realpath: cannot read /proc/self/fd or mnt_id in /proc/self/fdinfo; falling back to filepath.EvalSymlinks, checked against the opened file; files.read will not return real"
)

// startReadlink and startReadFile are the seams of the start test.
var (
	startReadlink = os.Readlink
	startReadFile = os.ReadFile
)

var (
	filesRealOnce sync.Once
	filesRealOK   bool
)

// filesReadRealAvailable reports whether the start test passed. It runs the test
// once. server.capabilities lists the feature files.read.real only when this is
// true (featuresFor).
func filesReadRealAvailable() bool {
	filesRealOnce.Do(func() { filesRealOK = probeOpenHandle() })
	return filesRealOK
}

// probeOpenHandle opens "/" and tests that the link of its descriptor under
// /proc/self/fd can be read and that its fdinfo holds a "mnt_id:" line. The
// exact steps are claustrum's own: the rows show only the two outcomes.
func probeOpenHandle() bool {
	f, err := os.Open("/")
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	fd := f.Fd()
	if _, err := startReadlink(fmt.Sprintf("/proc/self/fd/%d", fd)); err != nil {
		return false
	}
	info, err := startReadFile(fmt.Sprintf("/proc/self/fdinfo/%d", fd))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(info), "\n") {
		if strings.HasPrefix(line, "mnt_id:") {
			return true
		}
	}
	return false
}

// logFilesReadRealStart writes the start line once, before the first connection.
func logFilesReadRealStart() {
	if filesReadRealAvailable() {
		logInfof("%s", filesRealPassLine)
		return
	}
	logInfof("%s", filesRealFailLine)
}
