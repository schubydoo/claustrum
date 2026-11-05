package supervisor

import (
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/schubydoo/claustrum/internal/conn"
)

// The functions below let the tests of package main swap the seams of the manager
// and read the state of a process. Only tests call them. Each setter returns the
// value that it replaced. A value that the manager copies at construction
// (exitDrainGrace in spawn, procPruneAge, procPruneInterval, linkSpentAge and
// defaultKillWaitMs, the supersede grace, in NewManager) takes effect only for a later spawn or a later manager.

// SetSignalGroupForTest sets the signalGroup seam and returns the old one.
func SetSignalGroupForTest(f func(g *ProcGroup, proc *os.Process, signame string) error) (prev func(g *ProcGroup, proc *os.Process, signame string) error) {
	prev, signalGroup = signalGroup, f
	return prev
}

// SetExitDrainGraceForTest sets exitDrainGrace and returns the old value.
func SetExitDrainGraceForTest(d time.Duration) (prev time.Duration) {
	prev, exitDrainGrace = exitDrainGrace, d
	return prev
}

// ExitDrainGraceForTest returns exitDrainGrace.
func ExitDrainGraceForTest() time.Duration { return exitDrainGrace }

// SetKillReapGraceForTest sets killReapGrace and returns the old value.
func SetKillReapGraceForTest(d time.Duration) (prev time.Duration) {
	prev, killReapGrace = killReapGrace, d
	return prev
}

// SetStdinQueueCapForTest sets stdinQueueCap and returns the old value.
func SetStdinQueueCapForTest(n int) (prev int) {
	prev, stdinQueueCap = stdinQueueCap, n
	return prev
}

// SetDefaultKillWaitMsForTest sets defaultKillWaitMs and returns the old value.
func SetDefaultKillWaitMsForTest(ms int) (prev int) {
	prev, defaultKillWaitMs = defaultKillWaitMs, ms
	return prev
}

// SetMaxKillWaitMsForTest sets maxKillWaitMs and returns the old value.
func SetMaxKillWaitMsForTest(ms int) (prev int) {
	prev, maxKillWaitMs = maxKillWaitMs, ms
	return prev
}

// SetBeforeChildStartForTest sets the beforeChildStart seam and returns the old one.
func SetBeforeChildStartForTest(f func(id string)) (prev func(id string)) {
	prev, beforeChildStart = beforeChildStart, f
	return prev
}

// SetPastStartCheckForTest sets the pastStartCheck seam and returns the old one.
func SetPastStartCheckForTest(f func(id string)) (prev func(id string)) {
	prev, pastStartCheck = pastStartCheck, f
	return prev
}

// SetAfterChildStartForTest sets the afterChildStart seam and returns the old one.
func SetAfterChildStartForTest(f func(id string, pid int)) (prev func(id string, pid int)) {
	prev, afterChildStart = afterChildStart, f
	return prev
}

// SetConfineProcForTest sets the confineProc seam and returns the old one.
func SetConfineProcForTest(f func(*os.Process, ChildJobs) (*ProcGroup, error)) (prev func(*os.Process, ChildJobs) (*ProcGroup, error)) {
	prev, confineProc = confineProc, f
	return prev
}

// SetProcPruneForTest sets procPruneAge and procPruneInterval and returns the old
// values.
func SetProcPruneForTest(age, interval time.Duration) (prevAge, prevInterval time.Duration) {
	prevAge, prevInterval = procPruneAge, procPruneInterval
	procPruneAge, procPruneInterval = age, interval
	return prevAge, prevInterval
}

// SetLinkSpentAgeForTest sets linkSpentAge and returns the old value.
func SetLinkSpentAgeForTest(d time.Duration) (prev time.Duration) {
	prev, linkSpentAge = linkSpentAge, d
	return prev
}

// SetFarewellGraceCapMsForTest sets farewellGraceCapMs and returns the old value.
func SetFarewellGraceCapMsForTest(ms int) (prev int) {
	prev, farewellGraceCapMs = farewellGraceCapMs, ms
	return prev
}

// SetFarewellSweepHookForTest sets the farewellSweepHook seam. The hook gets a func
// that fires the grace timer of the farewell at once. A nil f clears the seam.
func SetFarewellSweepHookForTest(f func(fireTimer func())) {
	if f == nil {
		farewellSweepHook = nil
		return
	}
	farewellSweepHook = func(h *farewellHold) { f(func() { h.timer.Reset(0) }) }
}

// WriteFramesLockedForTest is writeFramesLocked.
func WriteFramesLockedForTest(c *conn.Conn, frames []StreamFrame) error {
	return writeFramesLocked(c, frames)
}

// AddFakeProcForTest puts a record with no child under id in the table of m and
// returns it. running and reaped give its state. A non-empty argv is the argv of a
// command that never starts.
func (m *Manager) AddFakeProcForTest(id string, running, reaped bool, argv ...string) *Proc {
	p := &Proc{id: id, subs: map[*conn.Conn]struct{}{}, running: running, reaped: reaped}
	p.stdinCond = sync.NewCond(&p.stdinMu)
	if len(argv) > 0 {
		p.cmd = exec.Command(argv[0], argv[1:]...)
	}
	m.mu.Lock()
	m.procs[id] = p
	m.mu.Unlock()
	return p
}

// AddRefusedChildForTest puts a live record under id in the table of m that no exit
// ends, with the child pid pid, and returns it.
func (m *Manager) AddRefusedChildForTest(id string, pid int) *Proc {
	p := &Proc{
		id: id, running: true, group: &ProcGroup{},
		cmd: &exec.Cmd{Process: &os.Process{Pid: pid}}, done: make(chan struct{}),
	}
	m.mu.Lock()
	m.procs[id] = p
	m.mu.Unlock()
	return p
}

// IDForTest returns the id of p.
func (p *Proc) IDForTest() string { return p.id }

// DoneForTest returns the channel that closes after the exit frame of p.
func (p *Proc) DoneForTest() <-chan struct{} { return p.done }

// CmdForTest returns the command of the child of p.
func (p *Proc) CmdForTest() *exec.Cmd { return p.cmd }

// GroupForTest returns the kill handle of p.
func (p *Proc) GroupForTest() *ProcGroup { return p.group }

// KillForTest sends KILL to p as signalIfLive does, with no killedBy, and reports
// whether p was live.
func (p *Proc) KillForTest() bool { return p.signalIfLive("KILL", "") }

// KilledByForTest returns the killedBy of p.
func (p *Proc) KilledByForTest() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.killedBy
}

// InDrainForTest reports whether p is in its exit drain: reaped, and its exit frame
// not out yet.
func (p *Proc) InDrainForTest() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reaped && p.running
}

// KillGroupAfterExitForTest is killGroupAfterExit.
func (p *Proc) KillGroupAfterExitForTest() error { return p.killGroupAfterExit() }

// FarewellPendingForTest reports whether p holds a pending farewell.
func (p *Proc) FarewellPendingForTest() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.farewell != nil
}

// FillStdinQueueForTest fills the stdin queue of p to its cap. A child that does not
// read stdin keeps the writer blocked on the first chunk, so the rest stays queued.
func (p *Proc) FillStdinQueueForTest() {
	p.stdinMu.Lock()
	p.stdinQ = append(p.stdinQ, make([]byte, 1<<20), make([]byte, stdinQueueCap))
	p.stdinQBytes += 1<<20 + stdinQueueCap
	p.stdinCond.Broadcast()
	p.stdinMu.Unlock()
}

// StdinAppliedForTest returns the stdinApplied count of p.
func (p *Proc) StdinAppliedForTest() uint64 {
	p.stdinMu.Lock()
	defer p.stdinMu.Unlock()
	return p.stdinApplied
}

// SignalGroupForTest returns the signalGroup seam.
func SignalGroupForTest() func(g *ProcGroup, proc *os.Process, signame string) error {
	return signalGroup
}
