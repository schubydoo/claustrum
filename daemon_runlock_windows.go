//go:build windows

package main

// claimRunDir is a no-op on Windows. The reference's Windows build (4534d86) does not
// compile the run-dir claim or the machine-identity code at all: its serve path never
// opens a lock file, so no daemon.lock is written, no owner record is persisted, and no
// predecessor is evicted (measured — the run-dir-claim and node-identity function bodies
// are absent from the Windows build, and the linked owner-record helpers only ever run
// against an in-memory handle that Windows never backs with a file). Windows relies on
// the socket remove-then-rebind handoff for mutual exclusion, where a second -serve
// leaves the incumbent alive. claustrum matches by doing nothing here; the returned
// release func is a no-op.
func claimRunDir(socket, role string) func() { return func() {} }
