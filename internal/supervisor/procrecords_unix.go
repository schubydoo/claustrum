//go:build linux || darwin

package supervisor

import "github.com/schubydoo/claustrum/internal/childrec"

// The two record steps of the shutdown that read the process table of the manager. The
// records themselves are in internal/childrec.

// AwaitRecordsRemoved waits until the exit goroutine of every managed process has
// removed its record, or until the bound of childrec.AwaitDone passes. The graceful
// shutdown calls it after it killed the children, so the daemon does not exit before
// the records are gone. It is a no-op for a manager with no run dir.
func (m *Manager) AwaitRecordsRemoved() {
	if m.RunDir == "" {
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
	childrec.AwaitDone(waits)
}

// ForgetKeptRecords removes the record of every managed process that still runs. The
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
func (m *Manager) ForgetKeptRecords() {
	if m.RunDir == "" {
		return
	}
	m.mu.Lock()
	pids := make([]int, 0, len(m.procs))
	for _, p := range m.procs {
		if p.IsLive() {
			pids = append(pids, p.pid)
		}
	}
	m.mu.Unlock()
	for _, pid := range pids {
		m.RemoveChildRecord(pid)
	}
}
