//go:build linux || darwin

package main

import (
	"errors"
	"io/fs"
	"os"
	"time"

	"github.com/schubydoo/claustrum/internal/logging"
)

// The part of the child registry that linux and darwin share: the handle on the run
// dir, the write of a record with its log lines, and the removal of a record when its
// child ends. How a record gets its identity is OS-specific (childrecord_linux.go,
// childrecord_darwin.go). On windows all of this is a no-op (childrecord_other.go).

// shutdownRecordWait bounds how long a graceful shutdown waits for its killed
// children to end, so that the end of each child removes its record before the
// daemon exits. The bound is claustrum's own and is not measured. Measured on Linux:
// the children folder of the reference is empty 0.1 s after a SIGTERM of the daemon
// and 1 s after a -stop. A var so a test can change it.
var shutdownRecordWait = time.Second

// holdRunDir opens the run dir and keeps it open for the life of the daemon. The
// records then follow the folder itself: after a rename of the run dir, a record goes
// under the new name and the old path is not made again (measured on Linux). It is a
// no-op for a manager with no run dir. The daemon calls it once at startup. A manager
// that never called it opens the run dir at its first record.
func (m *procManager) holdRunDir() {
	_, _ = m.runDirRoot()
}

// runDirRoot returns the os.Root of the run dir, opened once.
func (m *procManager) runDirRoot() (*os.Root, error) {
	m.runRootOnce.Do(func() {
		if m.runDir == "" {
			m.runRootErr = errors.New("no run dir")
			return
		}
		m.runRoot, m.runRootErr = os.OpenRoot(m.runDir)
	})
	return m.runRoot, m.runRootErr
}

// writeRecord writes the record of a just-spawned child and logs a failure. It never
// affects the spawn. The two log texts are the reference's (measured on Linux). The
// level tag before them is claustrum's own.
func (m *procManager) writeRecord(rec childRecord) {
	root, err := m.runDirRoot()
	if err == nil {
		err = writeChildRecordIn(root, rec)
	}
	switch {
	case err == nil:
	case errors.Is(err, errChildrenNotDir):
		logging.Errorf("[process.Registry] %s/%s is not a directory; recording nothing", m.runDir, childrenDirName)
	default:
		logging.Errorf("[process.Registry] cannot record child %d: %v", rec.Pid, err)
	}
}

// removeChildRecord removes the record of a child that has ended. spawn hands it to
// the exit goroutine, which calls it right after the wait on the child returns and
// before the pipe drain. Measured on Linux: the record of the reference is gone when
// the child ends, also while a grandchild holds the pipes and the exit frame is 5 s
// away.
//
// The delete is one plain remove of children/<pid>.json through the os.Root of the
// run dir. It is never recursive and it follows no symlink out of the run dir. The
// reference tries the removal also when it wrote no record, and logs the failure
// (measured), so this does the same.
func (m *procManager) removeChildRecord(pid int) {
	if m.runDir == "" || pid < 2 {
		return
	}
	root, err := m.runDirRoot()
	if err != nil {
		return
	}
	if err := removeChildRecordIn(root, pid); err != nil {
		logging.Warnf("[process.Registry] cannot remove record of child %d: %v", pid, err)
	}
}

// removeChildRecordIn removes children/<pid>.json below root, which is the run dir. It
// is one plain remove of one name, never recursive, and the os.Root keeps it inside the
// run dir. A record that does not exist is not an error. As 89cb6289 does, the remove
// goes through a children symlink that stays inside the run dir (Linux rows SLa, SLb,
// macOS KDa, KDb). A symlink that leads out is refused (measured).
func removeChildRecordIn(root *os.Root, pid int) error {
	err := root.Remove(childRecordName(pid))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// awaitRecordsRemoved waits until the exit goroutine of every managed process has
// removed its record, or until shutdownRecordWait passes. The graceful shutdown
// calls it after it killed the children, so the daemon does not exit before the
// records are gone. It is a no-op for a manager with no run dir.
func (m *procManager) awaitRecordsRemoved() {
	if m.runDir == "" {
		return
	}
	m.mu.Lock()
	waits := make([]chan struct{}, 0, len(m.procs))
	for _, p := range m.procs {
		if p.recordDone != nil {
			waits = append(waits, p.recordDone)
		}
	}
	m.mu.Unlock()
	deadline := time.NewTimer(shutdownRecordWait)
	defer deadline.Stop()
	for _, ch := range waits {
		select {
		case <-ch:
		case <-deadline.C:
			return
		}
	}
}

// forgetKeptRecords removes the record of every managed process that still runs. The
// graceful shutdown with -keep-children calls it for the children that it leaves
// alive, before it releases the run-dir lock. A kept child then has no record, so the next daemon on this socket does not
// take it for the child of a dead daemon and sends it no signal.
//
// This rule is claustrum's own, as the flag is. Without the rule the flag has no use
// on linux and darwin, because the reap of the next start ends every recorded child
// of a daemon that is gone. A Linux test ran the flag on claustrum only (measured): -stop
// sent no kill and removed the records of two children. A new daemon then sent them no
// signal, with the flag and without it.
//
// The rule covers the processes of the table only. A process whose id a later spawn
// took keeps its record (see spawn). The host cleaner does not read the records, so
// this rule does not change what it ends.
func (m *procManager) forgetKeptRecords() {
	if m.runDir == "" {
		return
	}
	m.mu.Lock()
	pids := make([]int, 0, len(m.procs))
	for _, p := range m.procs {
		if p.isLive() {
			pids = append(pids, p.pid)
		}
	}
	m.mu.Unlock()
	for _, pid := range pids {
		m.removeChildRecord(pid)
	}
}
