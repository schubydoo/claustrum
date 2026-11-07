//go:build linux || darwin

package childrec

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"syscall"
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

// HoldRunDir opens the run dir and keeps it open for the life of the daemon. The
// records then follow the folder itself: after a rename of the run dir, a record goes
// under the new name and the old path is not made again (measured on Linux). It is a
// no-op for a manager with no run dir. The daemon calls it once at startup. A manager
// that never called it opens the run dir at its first record.
func (r *Records) HoldRunDir() {
	_, _ = r.runDirRoot()
}

// runDirRoot returns the os.Root of the run dir, opened once.
func (r *Records) runDirRoot() (*os.Root, error) {
	r.runRootOnce.Do(func() {
		if r.RunDir == "" {
			r.runRootErr = errors.New("no run dir")
			return
		}
		r.runRoot, r.runRootErr = os.OpenRoot(r.RunDir)
	})
	return r.runRoot, r.runRootErr
}

// writeRecord writes the record of a just-spawned child and logs a failure. It never
// affects the spawn. The two log texts are the reference's (measured on Linux). The
// level tag before them is claustrum's own.
func (r *Records) writeRecord(rec childRecord) {
	r.writtenMu.Lock()
	defer r.writtenMu.Unlock()
	root, err := r.runDirRoot()
	if err == nil {
		err = writeChildRecordIn(root, rec)
	}
	switch {
	case err == nil:
		if r.written == nil {
			r.written = make(map[int]childRecord)
		}
		r.written[rec.Pid] = rec
	case errors.Is(err, errChildrenNotDir):
		logging.Errorf("[process.Registry] %s/%s is not a directory; recording nothing", r.RunDir, childrenDirName)
	default:
		logging.Errorf("[process.Registry] cannot record child %d: %v", rec.Pid, err)
	}
}

// RemoveChildRecord removes the record of a child that has ended. spawn hands it to
// the exit goroutine, which calls it right after the wait on the child returns and
// before the pipe drain. Measured on Linux: the record of the reference is gone when
// the child ends, also while a grandchild holds the pipes and the exit frame is 5 s
// away.
//
// The record goes only while the file still holds the start that this daemon wrote
// (c9125eb8, measured on Linux and macOS, see recordIdentity). A pid with no record of
// this daemon gets no removal and no log line (c9125eb8, measured on Linux and macOS).
// The same case through a children symlink is claustrum's own, not measured.
// 89cb6289 tried the removal and logged its failure.
//
// The delete is one plain remove of children/<pid>.json through the os.Root of the
// run dir. It is never recursive and it follows no symlink out of the run dir.
// writtenMu is held from the read to the remove. So a write of this daemon cannot
// replace the file in between.
func (r *Records) RemoveChildRecord(pid int) {
	if r.RunDir == "" || pid < 2 {
		return
	}
	r.writtenMu.Lock()
	defer r.writtenMu.Unlock()
	want, ok := r.written[pid]
	if !ok {
		return
	}
	delete(r.written, pid)
	root, err := r.runDirRoot()
	if err != nil {
		return
	}
	if err := removeChildRecordIn(root, want); err != nil {
		logging.Warnf("[process.Registry] cannot remove record of child %d: %v", pid, err)
	}
}

// maxRecordRead is the most bytes that the removal reads of a record. A larger file
// stays. The bound is claustrum's own, not measured: a record of this daemon is about
// 300 bytes.
const maxRecordRead = 64 << 10

// recordIdentity holds the members of a record that the removal compares. Measured on
// c9125eb8: a changed start keeps the record (Linux and macOS), and so does a changed
// start with a changed daemonStart (macOS). A changed node, host, argv0 or at removes it
// (Linux and macOS), and so does a changed instance, daemonStart or pid (Linux). A
// changed program removes it too (claustrum's own, not measured).
type recordIdentity struct {
	Start string `json:"start"`
}

func identityOf(rec childRecord) recordIdentity {
	return recordIdentity{Start: rec.Start}
}

// removeChildRecordIn removes children/<want.Pid>.json below root, which is the run
// dir, when the file holds want (holdsRecord). It is one plain remove of one name,
// never recursive, and the os.Root keeps it inside the run dir. A record that does
// not exist is not an error. A file that is not the record of want stays, and no line
// is logged (c9125eb8, measured on Linux).
func removeChildRecordIn(root *os.Root, want childRecord) error {
	name := childRecordName(want.Pid)
	if !holdsRecord(root, name, want) {
		return nil
	}
	err := root.Remove(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// recordCompared runs in holdsRecord after the compare and before the last Lstat. It is
// a seam for a test that swaps the file there. Production never reassigns it.
var recordCompared = func() {}

// holdsRecord reports whether name below root is a regular file that holds want.
// Measured on c9125eb8: a file that is not JSON stays (Linux), and so does a blank
// file (macOS). A link to a copy of the record, inside or outside the run dir, stays
// (Linux and macOS). The other tests below on the kind and the identity of the file
// are claustrum's own, not measured: a FIFO or a file of more than maxRecordRead
// bytes stays. The last Lstat shows that the name still holds the file
// that was read, just before the remove. No lock reaches a writer outside this
// daemon, so that test is the nearest form there.
func holdsRecord(root *os.Root, name string, want childRecord) bool {
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() {
		return false
	}
	// O_NONBLOCK: a FIFO put at the name after the Lstat does not hold the open.
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(f, maxRecordRead+1))
	if err != nil || len(data) > maxRecordRead {
		return false
	}
	var got recordIdentity
	if json.Unmarshal(data, &got) != nil || got != identityOf(want) {
		return false
	}
	recordCompared()
	now, err := root.Lstat(name)
	return err == nil && os.SameFile(now, opened)
}

// AwaitDone waits until each channel of waits is closed, or until shutdownRecordWait
// passes. The graceful shutdown of the manager calls it with the channels that the exit
// goroutines close when they removed their records, so the daemon does not exit before
// the records are gone.
func AwaitDone(waits []chan struct{}) {
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
