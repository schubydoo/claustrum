package git

import (
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

// holdKilledHelpers is false: no Windows row measures a repeat kill or the time of
// the reap of a killed helper of the content check (claustrum's own).
const holdKilledHelpers = false

// awaitHelperExit waits for the exit of the helper p on a handle of its own, and
// reaps nothing. The wait of exec.Cmd holds the process until then. It reports that
// it saw the exit. A failed open or wait reports false, and the gate of the helper
// then closes, so the helper gets no kill (claustrum's own, not measured).
func awaitHelperExit(p *os.Process) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(p.Pid))
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(h) }()
	ev, err := windows.WaitForSingleObject(h, windows.INFINITE)
	return err == nil && ev == windows.WAIT_OBJECT_0
}

// lacksExecBit is false on Windows: a file there has no execute bit, and the
// check has no such test (claustrum's own, not measured).
func lacksExecBit(fs.FileInfo) bool { return false }

// committedLinkFault finds no fault on Windows: the chain test of a committed
// link is measured on Linux and macOS only, and Windows keeps the check
// without it (claustrum's own, not measured).
func committedLinkFault(string, string) linkFault { return linkNoFault }
