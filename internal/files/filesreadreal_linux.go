package files

import (
	"fmt"
	"os"
	"strings"
	"sync"
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

// SetStartTestForTest replaces the reads of the start test and resets its
// cached answer. The returned func puts both back and resets the answer again.
// Only tests call it.
func SetStartTestForTest(link func(string) (string, error), read func(string) ([]byte, error)) (restore func()) {
	oldL, oldR := startReadlink, startReadFile
	startReadlink, startReadFile = link, read
	filesRealOnce = sync.Once{}
	return func() {
		startReadlink, startReadFile = oldL, oldR
		filesRealOnce = sync.Once{}
	}
}

// ReadRealAvailable reports whether the start test passed. It runs the test
// once. server.capabilities lists the feature files.read.real only when this is
// true (featuresFor).
func ReadRealAvailable() bool {
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
