package git

import (
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

// holdKilledHelpers is false: no Windows row measures a repeat kill or the time of
// the reap of a killed helper of the content check (claustrum's own).
const holdKilledHelpers = false

// awaitHelperExit waits for the exit of the helper p on the handle of p, and reaps
// nothing. It reports that it saw the exit. A failed wait reports false. The gate of
// the helper then closes, so the helper gets no kill, and the end of the check does
// not count it as gone (claustrum's own, not measured).
func awaitHelperExit(p *os.Process) bool {
	seen := false
	err := p.WithHandle(func(h uintptr) {
		ev, err := windows.WaitForSingleObject(windows.Handle(h), windows.INFINITE)
		seen = err == nil && ev == windows.WAIT_OBJECT_0
	})
	return err == nil && seen
}

// lacksExecBit is false on Windows: a file there has no execute bit, and the
// check has no such test (claustrum's own, not measured).
func lacksExecBit(fs.FileInfo) bool { return false }

// committedLinkFault finds no fault on Windows: the chain test of a committed
// link is measured on Linux and macOS only, and Windows keeps the check
// without it (claustrum's own, not measured).
func committedLinkFault(string, string) linkFault { return linkNoFault }
