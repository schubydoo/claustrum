package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// streamFrame is an id-less notification pushed to attached clients.
// ExitCode is a pointer so a legitimate 0 is emitted while stdout/stderr frames
// omit it entirely; Data is omitted on exit frames.
type streamFrame struct {
	Type      string `json:"type"`
	ProcessID string `json:"processId"`
	Stream    string `json:"stream"` // stdout | stderr | exit
	Seq       uint64 `json:"seq"`
	Data      string `json:"data,omitempty"`
	ExitCode  *int   `json:"exitCode,omitempty"`

	// lineBytes is the length of this frame AS SERIALIZED, including the
	// trailing newline — the unit the replay buffer accounts in. Unexported, so
	// encoding/json ignores it and no frame on the wire changes shape.
	lineBytes int64
}

// defaultBufferCap caps the total SERIALIZED FRAME BYTES — each frame's JSON
// line including its trailing newline — held in each per-process replay buffer. A long-running, high-throughput process would otherwise grow
// the buffer without bound for the daemon's entire lifetime (the buffer is never
// reclaimed while the procManager holds the entry).
//
// 16 MiB is the REFERENCE's value, not a tuning choice — do not change it
// without re-measuring. Driving 70 MiB through both daemons and calling
// reattach{fromSeq:0} showed the reference retaining ≈15.85 MiB across 1,915
// frames where claustrum's then-50 MiB retained ≈49.99 MiB across 5,718.
// firstSeq is wire-visible via reattach, so this bound is observable contract.
//
// CORRECTION, 2026-08-02: that measurement also concluded "the accounting method
// already matched — base64 length", and only the constant was changed. The unit
// was wrong too. The reference's retained-frame counts match a 16 MiB bound
// measured in the bytes a subscriber receives — the serialized frame plus its
// newline — where claustrum counted len(f.Data), the base64 payload alone. See
// bufBytes/emit below.
//
// The reason it read as matching is worth more than the bug. That run used
// ~8.7 KB frames, where the ~80-byte JSON envelope is under 1% — inside the noise
// of where a trim boundary lands. Re-measured at ~600-byte frames, where the
// envelope is ~12%, driving 27 MiB through each:
//
//	frame size   retained frames, reference   claustrum (before)
//	  ~8700 B                       1,438       1,447    control — 0.6% apart
//	   ~600 B                      19,195      20,962    9.2% apart
//
// The arithmetic identifies the ENVELOPE, and only the envelope. A 600-byte
// payload is 800 bytes of base64: 16 MiB / 800 = 20,971 ≈ claustrum's 20,962,
// while 16 MiB / (800 + ~80) = 19,065 and with the newline 19,043 — both within
// the slop of a ≈ against 19,195, so this division cannot tell you whether the
// trailing newline is counted. What settles the newline is the post-fix run:
// counting frame+newline puts both binaries on the SAME retained count exactly
// (19,194 each), where omitting it would leave claustrum ~22 frames adrift.
//
// Those figures are one frame apart between runs (19,195 then 19,194 for the
// reference) because frame boundaries depend on pipe scheduling and are not
// deterministic — see the note on frame boundaries in docs/PROTOCOL.md. The
// exact-agreement claim is about a single run comparing both binaries, not about
// a number that reproduces across runs.
//
// Be honest about how much that carries: the run-to-run spread is inferred from
// TWO observations, so "~22 frames adrift would be visible" rests on a weakly
// established noise floor. The load-bearing evidence is the SECOND arm — the
// ~8.7 KB control landing exactly (1,438 / 1,438) after the fix. Two independent
// arms agreeing exactly is much harder to get by luck than one, and that is what
// settles the trailing newline, not the 22-frame margin.
//
// A fixture can make a real divergence unmeasurable purely by choosing the wrong
// magnitude, and nothing about the earlier run looked wrong.
const defaultBufferCap int64 = 16 * 1024 * 1024

// stdinQueueCap bounds the per-process async stdin queue. A producer that outruns
// a slow or non-reading child blocks (backpressure) once this much data is queued,
// rather than letting the daemon's memory grow without bound. The reference
// applies the same kind of bound (its "stdin backpressure: queue full" guard); the
// exact threshold is a stderr-log edge, not part of the wire contract. var so
// tests can shrink it.
var stdinQueueCap = 8 * 1024 * 1024

type managedProc struct {
	id string
	// pid and startTime are captured once at spawn and never mutated, so they are
	// safe to read without p.mu. They back the CT-1 opt-in (process.spawn /
	// process.reattach with "wantPid":true).
	//
	// startTime is the daemon's wall clock (epoch seconds) captured at spawn,
	// returned identically on spawn and reattach for the same process. It is an
	// OPAQUE TOKEN for PID-reuse detection (CL-8): a client compares a persisted
	// daemon value against a later daemon value for the same id. Do NOT compare it
	// against an independently-read OS process start time (e.g. psutil
	// create_time) — the daemon's spawn-moment wall clock differs from the kernel's
	// process-creation time by the fork→time.Now() delta and a different clock
	// derivation, so an equality check would spuriously fail.
	pid       int
	startTime float64
	mu        sync.Mutex
	seq       uint64
	buffer    []streamFrame
	bufBytes  int64 // sum of f.lineBytes (serialized frame + newline) in buffer
	bufCap    int64 // per-instance override; 0 means use defaultBufferCap
	subs      map[*conn]struct{}
	running   bool
	// reaped is set as soon as cmd.Wait returns — the moment the kernel frees the
	// pid, and with it the process GROUP id once the last member goes. That is up
	// to exitDrainGrace before running clears, which is why the two fields exist
	// separately: running is the client's view, reaped is signal safety. The gap
	// is reachable exactly when a grandchild holds the pipe open, and a
	// grandchild that calls setsid() (which is what daemonizing means) leaves the
	// group, so the group really can empty and its id be reused mid-drain. Read
	// only under p.mu, and only by signalIfLive.
	reaped bool
	// exitedAt is stamped alongside running=false, so it dates the exit frame
	// rather than the reap — the two are up to exitDrainGrace apart. Zero while
	// the process is alive. Read only by pruneExited, under p.mu like running.
	exitedAt time.Time
	stdin    io.WriteCloser
	// cmd is set once in spawn's composite literal and never reassigned, so it is
	// safe to read without p.mu (waitReapAndDrain does). signalIfLive and
	// killGroupAfterExit hold p.mu across their p.cmd access to make the
	// reaped-check-and-signal atomic, not to protect this pointer.
	cmd   *exec.Cmd
	group *procGroup // OS handle for whole-tree teardown (Job Object on Windows)
	// done is closed once by the exit goroutine after the child is reaped, so
	// killAndWait can block until the process is actually gone.
	done chan struct{}

	// Async stdin: process.stdin enqueues here and returns immediately, while a
	// single stdinWriter goroutine drains the queue to the child's stdin pipe. This
	// keeps a slow or non-reading child from blocking the dispatch goroutine on a
	// synchronous pipe write — matching the reference, which returns success before
	// the child has read the data. Guarded by stdinMu (separate from mu).
	stdinMu     sync.Mutex
	stdinCond   *sync.Cond
	stdinQ      [][]byte
	stdinQBytes int
	stdinDone   bool
	stdinWarned bool
	// stdinApplied is the cumulative count of stdin bytes accepted for delivery,
	// the high-water mark that backs the stdin-offset idempotency contract (see
	// applyStdin). Guarded by stdinMu; surfaced on the wire as process.stdin's
	// "applied" and process.reattach's "stdinApplied".
	stdinApplied uint64
}

type procManager struct {
	mu    sync.Mutex
	procs map[string]*managedProc
	// stop ends the background prune sweep; stopOnce keeps close() idempotent so
	// a double shutdown cannot panic on a second close of the channel.
	stop     chan struct{}
	stopOnce sync.Once
	// pruneAge/pruneInterval are COPIED from the package vars at construction
	// rather than read live by the sweep. A sweep goroutine outlives its test
	// whenever a manager is built without a teardown, so reading the vars from
	// inside the loop makes any later test that shrinks them a data race — which
	// is exactly what -race caught. Copying confines each var to the constructing
	// goroutine.
	pruneAge      time.Duration
	pruneInterval time.Duration
}

// procPruneAge is how long a process stays reachable after it exits. Past this,
// process.reattach reports found:false and the id is free again — the reference
// drops the entry, along with its replay buffer, from the table.
//
// Provenance, which used to be labelled "probe-measured" as a whole and is not:
//
//	probe (5db5e4a)  an entry is still reachable 45s after its process exited,
//	                 and gone 960s after. That brackets the age to (45s, 960s].
//	                 It does NOT single out 900s, and no black-box observable
//	                 can. Both ends are observations, not inferences: the lower
//	                 one is a reattach answering found:true at 45s, re-measured
//	                 2026-08-02 because the bracket previously published a 20s
//	                 lower bound that nothing stated beside it supported.
//	pointer-class    the only duration constant in its pruneExited is 900s.
//	                 That is where the exact value comes from. Read, not probed.
//
// The value is very likely right; the label was wrong. Copied into the manager
// at construction. var so tests can shrink it.
var procPruneAge = 15 * time.Minute

// procPruneInterval is the sweep period of the background prune. The reference
// runs the same sweep from a time.NewTicker(60s) started by NewManager, on top
// of the inline call in Spawn — so an idle daemon prunes too, with no spawn to
// trigger it.
//
// Pointer-class, NOT probe-measured, and it cannot become probe-measured: the
// only wire effect of the sweep is that an aged-out id answers found:false, and
// an id that has aged out answers that way whatever schedule noticed it. No
// observable distinguishes a 60s ticker from a 30s or 120s one. What the probe
// DOES support is the "idle daemon prunes too" half — an entry disappears with
// no intervening process.spawn to trigger the inline call.
//
// Copied into the manager at construction, so a test must set it before
// newProcManager. var so tests can shrink it.
var procPruneInterval = time.Minute

func newProcManager() *procManager {
	m := &procManager{
		procs:         make(map[string]*managedProc),
		stop:          make(chan struct{}),
		pruneAge:      procPruneAge,
		pruneInterval: procPruneInterval,
	}
	go m.pruneLoop()
	return m
}

// pruneLoop sweeps long-exited processes out of the table on a timer, matching
// the goroutine the reference's NewManager starts. Without it an idle daemon
// would keep every dead process forever; spawn's inline sweep only ever prunes
// when new work arrives.
func (m *procManager) pruneLoop() {
	t := time.NewTicker(m.pruneInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			m.pruneExited()
		case <-m.stop:
			return
		}
	}
}

// close ends the prune sweep. Idempotent, and safe to call on a manager whose
// sweep never started.
func (m *procManager) close() {
	m.stopOnce.Do(func() { close(m.stop) })
}

// pruneExited drops every process that has been exited for longer than
// procPruneAge, freeing its replay buffer with it. A RUNNING process is immune
// at any age — probe-measured: a sleeper spawned 960s earlier is still
// found:true,running:true while a process that exited at the same moment is
// gone. After pruning, the id behaves exactly like one that never existed:
// reattach reports found:false and kill reports success:true, so no caller sees
// a new error.
func (m *procManager) pruneExited() {
	cutoff := time.Now().Add(-m.pruneAge)
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, p := range m.procs {
		p.mu.Lock()
		// exitedAt is zero until the exit frame goes out, which covers both a
		// running process and the brief window while one is being reaped.
		stale := !p.running && !p.exitedAt.IsZero() && p.exitedAt.Before(cutoff)
		p.mu.Unlock()
		if stale {
			// Safe to delete during range — Go defines this, and an entry removed
			// mid-iteration is simply not produced.
			delete(m.procs, id)
			logDebugf("[process.Manager] Pruned exited process %s", id)
		}
	}
}

func (m *procManager) get(id string) *managedProc {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.procs[id]
}

// isRunning reports the process's state AS CLIENTS SEE IT: true until the exit
// frame is emitted. During the bounded exit drain that is a window in which the
// process has already been reaped but still reports running — deliberately, to
// match the reference. Use isReaped, not this, to decide whether it is safe to
// signal.
func (p *managedProc) isRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

// signalGroup is a seam over procGroup.signal so the "do not signal a reaped
// process" guards are testable without actually delivering a signal to whatever
// now owns a recycled pgid — which is precisely the accident being guarded
// against. Production never reassigns it.
var signalGroup = func(g *procGroup, proc *os.Process, signame string) {
	g.signal(proc, signame)
}

// signalIfLive delivers signame to the process group unless the process has
// already been reaped. p.mu is held across BOTH the check and the delivery: an
// isReaped() that returns before the signal leaves a gap for the exit goroutine
// to reap in between, which is the very race the check exists to prevent. Every
// signal site goes through here for that reason.
//
// One window remains and cannot be closed at this layer: the kernel frees the
// pid inside cmd.Wait, a moment before the exit goroutine can take p.mu and set
// reaped. Signaling by pid on POSIX is racy by construction — nothing short of a
// pidfd fixes it, and a pidfd cannot address a process GROUP.
//
// This guard is a claustrum-only judgement, not a measured parity claim. The
// comment used to read "the reference has no guard here at all", which asserts
// an ABSENCE — no black-box probe can establish that, and it kept getting sent
// back for re-measurement it can never satisfy. What is defensible: the guard
// can only ever suppress a signal, so claustrum's behaviour here is at most
// narrower than the reference's, never wider.
func (p *managedProc) signalIfLive(signame string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.running || p.reaped || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	signalGroup(p.group, p.cmd.Process, signame)
}

// killGroupAfterExit SIGKILLs the process group once the managed process itself
// has already exited, sweeping up any child it left behind. Deliberately does
// NOT take the reaped guard signalIfLive applies: by construction it runs after
// the reap, so that guard would make it a no-op and the orphans would survive.
//
// This is the one place claustrum knowingly signals a pgid whose leader is gone.
// It runs immediately after p.done fires, so the window before the kernel could
// recycle the pgid is as small as it can be.
//
// What is measured is the OUTCOME, not the absence of a guard: the reference
// also sweeps up a grandchild left behind after the leader exits (#194). The
// comment used to say "the reference has no such guard anywhere" — an absence
// assertion no probe can confirm. Matching the observable sweep is the claim;
// how the reference arrives at it is not something this comment can know.
func (p *managedProc) killGroupAfterExit() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd == nil || p.cmd.Process == nil {
		return
	}
	signalGroup(p.group, p.cmd.Process, "KILL")
}

// emit assigns the next per-process seq, buffers the frame, and fans it out to
// every attached client. The buffer retains frames for later reattach, capped
// at defaultBufferCap (or the per-instance bufCap): the oldest frames are
// dropped, and firstSeq advances, when
// the cap is exceeded, matching the reattach contract on the wire.
func (p *managedProc) emit(f streamFrame) {
	p.mu.Lock()
	p.seq++
	f.Seq = p.seq
	f.Type = "stream"
	f.ProcessID = p.id
	// Marshal HERE, under the lock, because the accounting unit is the serialized
	// line and the seq that goes into it is assigned here. The same bytes are
	// reused for the fan-out below, so this is not an extra marshal — it moved.
	//
	// The COUNT is unchanged; the lock hold is not. p.mu is now held across a
	// marshal of up to ~44 KB of base64 per frame, where it was released first.
	// That is the same lock reattach takes to swap the subscriber set and the one
	// the stdout and stderr readers contend on, so under a high-throughput process
	// the two emitters now serialize on marshal time as well as on bookkeeping.
	// Unavoidable as written — the seq that goes into the bytes is assigned under
	// the lock — and stated here so anyone profiling emit finds it rather than
	// discovering it.
	//
	// json.Marshal, not an Encoder: this is the encode that carries EVERY live
	// stream frame, and its HTML escaping is inherited wire behaviour — see
	// docs/ARCHITECTURE.md → "Inherited wire bytes" and the note on
	// conn.writeJSON.
	b, merr := json.Marshal(f)
	if merr == nil {
		b = append(b, '\n')
	}
	f.lineBytes = int64(len(b))
	p.buffer = append(p.buffer, f)
	p.bufBytes += f.lineBytes
	cap := p.bufCap
	if cap == 0 {
		cap = defaultBufferCap
	}
	// Trim oldest frames while over cap, keeping at least the frame just added.
	for len(p.buffer) > 1 && p.bufBytes > cap {
		p.bufBytes -= p.buffer[0].lineBytes
		p.buffer = p.buffer[1:]
	}
	subs := make([]*conn, 0, len(p.subs))
	for c := range p.subs {
		subs = append(subs, c)
	}
	p.mu.Unlock()
	// The frames are identical per conn, so one marshal serves every subscriber.
	// json.Marshal of a streamFrame cannot realistically fail (strings + ints);
	// if it somehow does, every subscriber would have failed the same way, so
	// mirror the old per-conn behavior: log + detach each.
	for _, c := range subs {
		err := merr
		if err == nil {
			err = c.writeLine(b)
		}
		if err != nil {
			logWarnf("[frameSink] write failed, detaching: %v", err)
			p.mu.Lock()
			delete(p.subs, c)
			p.mu.Unlock()
		}
	}
}

// spawn starts a child process in its own process group and begins streaming. It
// returns the managedProc so the caller can read its (immutable) pid/startTime
// for the CT-1 opt-in; the wire reply is otherwise unaffected.
func (m *procManager) spawn(c *conn, id, command string, args []string, cwd string, env map[string]string) (*managedProc, error) {
	// Read the drain cap ONCE, here in the caller's goroutine, rather than from
	// inside the exit waiter. The waiter outlives the request — and outlives the
	// test that spawned it — so reading the package var there races any later
	// test that shrinks it. macOS CI caught exactly that; the same confinement is
	// why procManager copies its prune tunables at construction.
	grace := exitDrainGrace
	// The reference prunes inline here as well as on its ticker, so a busy daemon
	// sheds long-dead entries without waiting for the next sweep.
	m.pruneExited()
	cmd := exec.Command(command, args...)
	if cwd != "" {
		// Stat the cwd before exec so an unusable one names the directory at
		// fault, as the reference does, instead of Go's `fork/exec <command>:
		// …` which names the command. The two failures have DIFFERENT shapes —
		// probe-measured against the reference at 5db5e4a, 2026-07-30:
		//
		//	missing dir   -> "chdir <p>: stat <p>: no such file or directory"
		//	cwd is a file -> "chdir <p>: not a directory"   (no "stat <p>:" part)
		//
		// so the stat error is wrapped but the not-a-directory case is its own
		// message rather than a wrapped ENOTDIR.
		fi, err := os.Stat(cwd)
		if err != nil {
			return nil, fmt.Errorf("chdir %s: %w", cwd, err)
		}
		if !fi.IsDir() {
			return nil, fmt.Errorf("chdir %s: not a directory", cwd)
		}
		cmd.Dir = cwd
	}
	cmd.Env = buildEnv(env)
	cmd.SysProcAttr = newSysProcAttr()

	// Deliberately os.Pipe rather than cmd.StdoutPipe/StderrPipe. cmd.Wait closes
	// the pipes it creates itself, which forces the "drain fully, then Wait"
	// order and leaves no way to learn the moment the process exited — the exact
	// thing the bounded drain below needs. Handing exec a plain *os.File instead
	// passes the descriptor straight to the child: cmd.Wait then returns at
	// process exit and leaves these read ends alone, so their lifetime is ours.
	stdoutR, stdoutW, err := osPipe()
	if err != nil {
		return nil, err
	}
	stderrR, stderrW, err := osPipe()
	if err != nil {
		closeAll(stdoutR, stdoutW)
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = stdoutW, stderrW
	stdin, err := cmdStdinPipe(cmd)
	if err != nil {
		closeAll(stdoutR, stdoutW, stderrR, stderrW)
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		logErrorf("[process.Manager] Failed to start process %s: %v", id, err)
		closeAll(stdoutR, stdoutW, stderrR, stderrW)
		return nil, err
	}
	// The child holds its own copies now. Drop ours, or the read ends never see
	// EOF even after every process in the tree has exited.
	closeAll(stdoutW, stderrW)
	// Stamp the start time the instant the child exists, so the CT-1 startTime is
	// the process's birth moment (within ms), not a later bookkeeping point.
	startTime := float64(time.Now().UnixNano()) / 1e9
	logInfof("[process.Manager] Process %s started, PID=%d, command=%s", id, cmd.Process.Pid, command)
	met.spawns.Add(1)

	// Confine the child (and its descendants) so kill can tear down the whole
	// tree. On Unix this is the process group from newSysProcAttr; on Windows a
	// Job Object. A failure here is non-fatal — kill falls back to the parent.
	group, err := confineProcess(cmd.Process)
	if err != nil {
		logWarnf("[process.Manager] process-group confinement failed for %s: %v", id, err)
	}

	p := &managedProc{
		id:        id,
		pid:       cmd.Process.Pid,
		startTime: startTime,
		subs:      map[*conn]struct{}{c: {}},
		running:   true,
		stdin:     stdin,
		cmd:       cmd,
		group:     group,
		done:      make(chan struct{}),
	}
	p.stdinCond = sync.NewCond(&p.stdinMu)
	m.mu.Lock()
	// Reusing a live id replaces the registry entry (matching the reference:
	// both spawns succeed). The previous process would otherwise be orphaned —
	// unreachable via kill/stdin/reattach (which now key to the new process) and
	// missed by killAll — so we tear its tree down here rather than leak it. We
	// first drop its subscribers so its teardown frames (a late exit/stdout under
	// the now-reused id) don't reach clients. OS-level only — no wire frame
	// change; an intentional divergence from the reference, which leaves the old
	// process running (see docs/PROTOCOL.md).
	if old := m.procs[id]; old != nil {
		old.mu.Lock()
		old.subs = map[*conn]struct{}{}
		old.mu.Unlock()
		// Skip exited children — their pgid may already be recycled (see kill).
		old.signalIfLive("KILL")
	}
	m.procs[id] = p
	m.mu.Unlock()

	go p.stdinWriter()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); pumpStream(p, "stdout", stdoutR) }()
	go func() { defer wg.Done(); pumpStream(p, "stderr", stderrR) }()
	go p.waitReapAndDrain(&wg, stdoutR, stderrR, grace)
	return p, nil
}

// waitReapAndDrain runs after spawn in its own goroutine: it waits for the child
// to exit and reap, runs the bounded stdout/stderr drain, flips running to false,
// tears down stdin and the OS group handle, then emits the exit frame. Extracted
// from spawn so the lifecycle reads as one-level steps; behavior is unchanged.
func (p *managedProc) waitReapAndDrain(wg *sync.WaitGroup, stdoutR, stderrR *os.File, grace time.Duration) {
	// cmd.Wait returns the moment the process itself exits — it no longer
	// waits on the pipes, because they are ours now.
	err := p.cmd.Wait()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode() // -1 when terminated by signal
		} else {
			code = -1
		}
	}
	// The pid is now free for the kernel to reuse, and the pgid will be too
	// once the last group member goes. Record that BEFORE the drain window
	// opens, so no signal path can target a recycled id while we wait.
	p.mu.Lock()
	p.reaped = true
	p.mu.Unlock()
	// A child can leave a grandchild holding the same stdout — `npm run dev &`,
	// anything that daemonizes. The pipes then stay open long after the process
	// we spawned is gone. The reference gives that drain exactly 5 seconds and
	// then closes the read ends, so the exit frame lands on time and the
	// grandchild's next write fails with EPIPE. Waiting for EOF instead (what
	// claustrum did) means the exit frame is delayed for as long as the
	// grandchild lives, which for a dev server is "never".
	drained := make(chan struct{})
	go func() { wg.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(grace):
		closeAll(stdoutR, stderrR)
	}
	// Both pumps have returned by here: either they hit EOF on their own, or
	// the close above turned their blocked Read into ErrClosed. That barrier
	// is what keeps an output frame from overtaking the exit frame — no flag
	// on the frame path required, and no seq number burnt on a dropped frame.
	<-drained
	closeAll(stdoutR, stderrR) // no-op on the drained path; idempotent
	// running flips only now, not when cmd.Wait returned. Probe-measured: the
	// reference still reports running:true two seconds into the drain window
	// and false only once the exit frame is out.
	p.mu.Lock()
	p.running = false
	p.exitedAt = time.Now() // starts the procPruneAge clock
	p.mu.Unlock()
	// Stop the stdin writer and wake any producer blocked on a full queue; the
	// child's stdin pipe is closed by cmd.Wait, so further writes would fail.
	p.stdinMu.Lock()
	p.stdinDone = true
	p.stdinCond.Broadcast()
	p.stdinMu.Unlock()
	// Release the OS group handle now the child is gone. On Windows this drops
	// the Job Object's last handle, reaping any descendants it left behind.
	p.group.close()
	logInfof("[process.Manager] Process %s exited with code %d", p.id, code)
	met.processExits.Add(1)
	p.emit(streamFrame{Stream: "exit", ExitCode: &code})
	// Signal any killAndWait waiter now the child is fully reaped.
	close(p.done)
}

func pumpStream(p *managedProc, name string, r io.Reader) {
	logDebugf("[process.Manager] Starting %s streaming for process %s", name, p.id)
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			met.streamBytes.Add(int64(n))
			p.emit(streamFrame{
				Stream: name,
				Data:   base64.StdEncoding.EncodeToString(buf[:n]),
			})
		}
		if err != nil {
			// EOF is the normal end of a stream and says nothing. Anything else —
			// a closed pipe forced by the drain cap, an I/O error — is why the
			// output stopped, and without it a truncated stream looks identical to
			// a clean one. The reference logs this; claustrum returned silently.
			if !errors.Is(err, io.EOF) {
				logWarnf("[process.Manager] %s read error for process %s: %v", name, p.id, err)
			}
			return
		}
	}
}

// writeStdin enqueues data for asynchronous delivery to the child's stdin and
// returns true if the process exists and has a stdin pipe. The actual pipe write
// happens on the stdinWriter goroutine, so a slow/non-reading child never blocks
// the caller (matching the reference's async stdin). Returns false for an unknown
// process or one without stdin.
func (m *procManager) writeStdin(id string, data []byte) bool {
	p := m.get(id)
	if p == nil || p.stdin == nil {
		return false
	}
	met.stdinBytes.Add(int64(len(data)))
	p.enqueueStdin(data)
	return true
}

// enqueueStdin appends data to the async stdin queue and returns immediately. If
// the queue is already at capacity it blocks (backpressure) until the writer
// drains enough room, logging the reference's "queue full" guard once. A single
// write larger than the cap is still accepted when the queue is empty, so it can
// never deadlock waiting for a drain that cannot start.
func (p *managedProc) enqueueStdin(data []byte) {
	p.stdinMu.Lock()
	p.enqueueStdinLocked(data)
	p.stdinMu.Unlock()
}

// enqueueStdinLocked is enqueueStdin's body with p.stdinMu already held, so the
// stdin-offset path (applyStdin) can decide the offset and enqueue atomically
// under a single lock. cond.Wait releases stdinMu while parked, so the exit
// goroutine can still set stdinDone and wake it.
func (p *managedProc) enqueueStdinLocked(data []byte) {
	for !p.stdinDone && p.stdinQBytes > 0 && p.stdinQBytes+len(data) > stdinQueueCap {
		if !p.stdinWarned {
			logWarnf("[process.Manager] stdin backpressure: queue full for %s", p.id)
			p.stdinWarned = true
		}
		p.stdinCond.Wait()
	}
	if p.stdinDone {
		return
	}
	p.stdinQ = append(p.stdinQ, data)
	p.stdinQBytes += len(data)
	p.stdinCond.Broadcast()
}

// applyStdin implements the stdin-offset idempotency contract (advertised as the
// "process.stdin.offset" capability). offset is the byte position the caller
// believes this data starts at; nil means "append at the current high-water"
// (the legacy, offset-less behavior). It returns the new cumulative applied byte
// count, plus:
//   - duplicate=true when the data is wholly at-or-before what's already applied
//     (nothing new is enqueued; applied is unchanged), and
//   - gap=true when offset is ahead of applied (a hole that would drop input —
//     the caller must resend from `applied`; nothing is enqueued).
//
// A partial overlap (offset < applied < offset+len) enqueues only the fresh tail
// data[applied-offset:] and advances applied to offset+len. The decision and the
// enqueue happen under one stdinMu hold so concurrent stdin calls can't interleave
// against a stale counter.
func (p *managedProc) applyStdin(data []byte, offset *uint64) (applied uint64, duplicate, gap bool) {
	p.stdinMu.Lock()
	cur := p.stdinApplied
	if offset != nil {
		off := *offset
		if off > cur {
			p.stdinMu.Unlock()
			return cur, false, true
		}
		if skip := cur - off; skip > 0 {
			if skip >= uint64(len(data)) {
				p.stdinMu.Unlock()
				return cur, true, false
			}
			data = data[skip:]
		}
	}
	p.enqueueStdinLocked(data)
	p.stdinApplied += uint64(len(data))
	applied = p.stdinApplied
	p.stdinMu.Unlock()
	met.stdinBytes.Add(int64(len(data)))
	return applied, false, false
}

// stdinWriter drains the async queue to the child's stdin in FIFO order for the
// life of the process. A write error (child gone / pipe closed) or stdinDone (set
// when the process exits) stops it and discards any unsent data.
func (p *managedProc) stdinWriter() {
	p.stdinMu.Lock()
	for {
		for len(p.stdinQ) == 0 && !p.stdinDone {
			p.stdinCond.Wait()
		}
		if p.stdinDone {
			p.stdinQ, p.stdinQBytes = nil, 0
			p.stdinCond.Broadcast()
			p.stdinMu.Unlock()
			return
		}
		chunk := p.stdinQ[0]
		p.stdinQ = p.stdinQ[1:]
		p.stdinQBytes -= len(chunk)
		p.stdinCond.Broadcast() // wake producers blocked on a full queue
		p.stdinMu.Unlock()

		_, err := p.stdin.Write(chunk)

		p.stdinMu.Lock()
		if err != nil {
			// The whole queue is discarded here — everything the client sent and
			// got success:true for, that the child will now never see. Silently
			// dropping it left no trace at all; the reference logs the write error
			// that caused it.
			logWarnf("[process.Manager] drainStdin %s: write error: %v", p.id, err)
			p.stdinQ, p.stdinQBytes, p.stdinDone = nil, 0, true
			p.stdinCond.Broadcast()
			p.stdinMu.Unlock()
			return
		}
	}
}

// kill is best-effort and signals the whole process group (OS-specific). An
// already-exited child is skipped: once cmd.Wait has reaped it, its Unix pgid
// can be recycled, so a late negative-pid signal could hit an unrelated
// process group. (Windows is immune — the job handle pins identity.) OS-level
// hardening only; the wire reply does not depend on the signal side effect.
func (m *procManager) kill(id, signal string) {
	p := m.get(id)
	if p == nil {
		return
	}
	p.signalIfLive(signal)
}

// defaultKillWaitMs is the graceful-signal grace killAndWait uses when the caller
// sends no timeoutMs (or a non-positive one).
//
// Probe-measured at 3000ms against the reference — and re-measured 2026-08-02
// against the PIN, `5db5e4a`, because the original run used `7c2f88d` and the
// comment did not say so. Omitting timeoutMs entirely against a SIGTERM-ignoring
// child answers at 3.4s, where an explicit timeoutMs:1000 answers at 1.4s; the
// same ~0.4s harness overhead in both rows is what makes the pair readable, and
// the 1000ms row is the control proving the harness can separate two graces at
// all. var so tests can shrink it.
var defaultKillWaitMs = 3000

// maxKillWaitMs is the ceiling killAndWait clamps a caller's timeoutMs to. This
// is REFERENCE-REACHABLE and was wrong: a caller asking for 45s got 45s here and
// 30s there, so the exit frame arrived 15s late.
//
// CORRECTION, 2026-08-02: this shipped at 600000 with a comment saying the
// reference's "exact ceiling is above the ~90s we could observe, so this generous
// cap only bites on adversarial input, never a real client". Both halves were
// wrong — the ceiling is 30s, well inside what a real client sends, and it was
// observable all along. An earlier sweep had also filed this finding as disproved
// without recording the observation behind the dismissal, which is why it
// survived: a dismissal needs its evidence written down as much as a
// confirmation does.
//
// Measured against 5db5e4a with a SIGTERM-ignoring child, so the grace must run
// out in full. Elapsed to the killAndWait reply, ~0.4s of spawn+reply overhead
// included:
//
//	timeoutMs   reference   claustrum (before)
//	     2000       2.4s       2.4s     (control — both honour it)
//	    29500      29.9s      29.9s     (below the ceiling)
//	    30500      30.0s      30.9s     (reference stops; claustrum does not)
//	    45000      30.0s      45.4s     (constant, not proportional)
//
// For a child that NEVER exits the observable is elapsed time alone, so no golden
// or battery id can catch a regression there: the reply is
// {"found":true,"died":true,"escalated":true} whatever the ceiling is. Only a
// differential probe against the reference can see this at all.
//
// The body is NOT always identical, though, and the first probe here could not
// see that — its fixture excluded the only window where it matters. A child that
// ignores SIGTERM and then self-exits BETWEEN the clamp and the requested grace
// takes a different branch: with escalate:true the omitempty `escalated` key
// appears, and with escalate:false `died` flips true→false. Measured against
// 5db5e4a with a child exiting at 35s and timeoutMs:45000 — the reference
// escalates at 30s and claustrum now matches on both arms, with sub-clamp arms as
// the control.
//
// Honest bound: black-box timing places the ceiling in (29500, 30500] and cannot
// resolve it further, because ~0.4s of overhead swamps a finer bracket. 30000 is
// the only round value in that interval. An earlier audit reached the same value
// the same way, from a WIDER bracket — that is agreement, not independent
// confirmation, and borrowing confidence from it would be the mistake this
// comment exists to avoid. Do not restate this as "measured exactly 30000ms".
// var, not const, for the same reason defaultKillWaitMs is one: a test has to be
// able to shrink it to prove the clamp reaches the actual wait. Production never
// assigns it.
var maxKillWaitMs = 30000 // 30s

// killReapGrace bounds the wait for a SIGKILL'd process to be reaped after
// escalation. It is deliberately independent of the caller's grace — NOT
// necessarily larger than it, as this said until 2026-08-02: a caller may send
// any timeoutMs up to the 30s ceiling, so "larger than" was false for anything
// over 5s and was false before that change too. The two never interact: the reap
// bound runs strictly after the grace, sequentially.
//
// It is small because SIGKILL is uncatchable and the reap is near-instant, so it
// only guards against a pathological unreapable child (e.g. stuck in
// uninterruptible sleep) wedging the dispatch goroutine. var so tests can shrink
// it.
//
// SEVEN seconds, matching the reference — MEASURED 2026-08-06, black-box, not
// inferred. Observing it needs a child SIGKILL cannot reap, which a
// pipe-holding grandchild does NOT produce: the exit drain closes the read ends
// at 5s on both binaries and the reply lands at 5.01s either way, so that route
// proves nothing. The fixture that works is real uninterruptible sleep — a read
// against a dm-delay device on an ephemeral VM. With timeoutMs 500:
//
//	reference        : reply at 7.51s  -> 7s grace
//	claustrum (was)  : reply at 5.51s  -> 5s grace
//	claustrum (this) : reply at 7.51s  -> 7s grace, re-measured on the same fixture
//	control          : an ordinary sleeper, 0.00s on both
//
// The reply JSON is the same shape on both ({"found":true,"died":false,
// "escalated":true}); what differed was when it arrived — and, for a child that
// IS reaped between 5s and 7s, whether "died" is false or true.
var killReapGrace = 7 * time.Second

// exitDrainGrace bounds how long the exit frame waits for stdout/stderr to reach
// EOF after the spawned process itself has exited. Only a grandchild that
// inherited the pipe can hold them open that long, and the reference gives it
// exactly this much before closing the read ends and emitting exit anyway
// (measured at 5s against 5db5e4a; its Spawn waiter pairs os.Process.wait with a
// 5s time.NewTimer). var so tests can shrink it.
var exitDrainGrace = 5 * time.Second

// osPipe and cmdStdinPipe are seams over the three pipe constructions in spawn.
// Each fails only on fd exhaustion, so their cleanup paths — which must close the
// pipes already made, or spawn leaks descriptors on every failure — are otherwise
// unreachable in a test. Production never reassigns either.
var (
	osPipe       = os.Pipe
	cmdStdinPipe = (*exec.Cmd).StdinPipe
)

// closeAll closes every file, ignoring errors. Used on the spawn error paths and
// to force a stalled drain to end. Closing an *os.File twice is safe — the second
// call returns ErrClosed without touching the descriptor — so callers may close
// defensively without tracking whether a pump got there first.
func closeAll(fs ...*os.File) {
	for _, f := range fs {
		_ = f.Close()
	}
}

// clampKillWaitMs maps a caller's timeoutMs onto the grace killAndWait actually
// waits: non-positive → the default (probe-verified: 0 and -100 both wait 3000ms),
// otherwise the value verbatim up to maxKillWaitMs.
func clampKillWaitMs(ms int) int {
	if ms <= 0 {
		return defaultKillWaitMs
	}
	if ms > maxKillWaitMs {
		return maxKillWaitMs
	}
	return ms
}

// killAndWait signals the process (default SIGTERM) and blocks until it is gone,
// waiting up to grace for the graceful signal to take effect. If it is still alive
// after grace and escalate is true, it force-kills with SIGKILL and reports
// escalated; if escalate is false, it leaves the process running and reports
// died:false. It reports:
//   - found:         the id was known
//   - died:          the process is now gone
//   - alreadyExited: it had already exited before we signalled (no kill needed)
//   - escalated:     it ignored the graceful signal and had to be SIGKILL'd
//
// The wire result is killAndWaitResult; an unknown id is (false,false,false,false).
func (m *procManager) killAndWait(id, signal string, grace time.Duration, escalate bool) (found, died, alreadyExited, escalated bool) {
	p := m.get(id)
	if p == nil {
		return false, false, false, false
	}
	if !p.isRunning() {
		return true, true, true, false
	}
	p.signalIfLive(signal)
	select {
	case <-p.done:
		if escalate {
			// The reference SIGKILLs the group even when the graceful signal
			// already did the job. Measured at 5db5e4a with a child that
			// backgrounds a sleeper: killAndWait with escalate:true leaves no
			// grandchild alive, escalate:false spares it — and the child itself
			// dies promptly either way, so this is not the post-grace escalation
			// below, it is an unconditional whole-tree sweep.
			//
			// Side effect only: every returned flag is unchanged, so the reply
			// frame is byte-identical to what it was before.
			p.killGroupAfterExit()
		}
		return true, true, false, false
	case <-time.After(grace):
	}
	// Still alive after the grace window. With escalate:false the caller wants a
	// best-effort graceful kill only — leave the process running and report it.
	if !escalate {
		return true, false, false, false
	}
	// The graceful signal did not finish the job within the grace — force-kill the
	// group.
	//
	// signalIfLive is deliberately NOT used here. By this point the child is very
	// often already reaped: the graceful signal worked, but a grandchild still
	// holding the stdout pipe keeps p.done pending until the exit drain gives up.
	// signalIfLive's reaped guard would then make the escalation a silent no-op,
	// which is precisely how claustrum came to leave grandchildren running where
	// the reference kills them. Measured: killAndWait with escalate:true left the
	// backgrounded sleeper alive on claustrum and dead on the reference.
	p.killGroupAfterExit()
	select {
	case <-p.done:
		return true, true, false, true
	case <-time.After(killReapGrace):
		// A SIGKILL that hasn't reaped (e.g. uninterruptible sleep) — don't wedge
		// the dispatch goroutine forever; report not-yet-dead.
		return true, false, false, true
	}
}

// reattach replays buffered frames with seq > fromSeq to c, (re)subscribes c for
// future frames, and reports the buffer/running state. It also returns the
// managedProc (nil when not found) so the caller can read its immutable
// pid/startTime for the CT-1 opt-in, and the process's cumulative stdinApplied so
// a reconnecting client can resume stdin from the right offset (0 when not found).
func (m *procManager) reattach(c *conn, id string, fromSeq uint64) (p *managedProc, found, running bool, firstSeq, lastSeq, stdinApplied uint64) {
	p = m.get(id)
	if p == nil {
		return nil, false, false, 0, 0, 0
	}
	met.reattaches.Add(1)
	p.mu.Lock()
	// A reattach TRANSFERS the frame stream to the new connection; it does not
	// add a second listener. Measured at 5db5e4a: after a reattach, output
	// produced by the process reaches only the reattaching connection, while the
	// previously attached one stops receiving. claustrum fanned out to both, so
	// a resumed session double-delivered every frame to whichever old connection
	// was still open — and the replay the new connection just received made those
	// duplicates overlap.
	//
	// The map is replaced rather than cleared entry-by-entry so the old set is
	// dropped atomically under p.mu, with no window where a frame could reach a
	// half-emptied set.
	p.subs = map[*conn]struct{}{c: {}}
	running = p.running
	var replay []streamFrame
	for _, f := range p.buffer {
		if f.Seq > fromSeq {
			replay = append(replay, f)
		}
	}
	if len(p.buffer) > 0 {
		firstSeq = p.buffer[0].Seq
		lastSeq = p.buffer[len(p.buffer)-1].Seq
	}
	p.mu.Unlock()
	p.stdinMu.Lock()
	stdinApplied = p.stdinApplied
	p.stdinMu.Unlock()
	for _, f := range replay {
		if err := c.writeJSON(f); err != nil {
			logWarnf("[frameSink] replay write failed, detaching: %v", err)
			p.mu.Lock()
			delete(p.subs, c)
			p.mu.Unlock()
			break
		}
	}
	return p, true, running, firstSeq, lastSeq, stdinApplied
}

func (m *procManager) detachConn(c *conn) {
	m.mu.Lock()
	procs := make([]*managedProc, 0, len(m.procs))
	for _, p := range m.procs {
		procs = append(procs, p)
	}
	m.mu.Unlock()
	for _, p := range procs {
		p.mu.Lock()
		delete(p.subs, c)
		p.mu.Unlock()
	}
}

// runningCount reports how many managed processes are still alive — used for the
// honest -keep-children shutdown log line. Same m→p lock order as killAll.
func (m *procManager) runningCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, p := range m.procs {
		if p.isRunning() {
			n++
		}
	}
	return n
}

func (m *procManager) killAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.procs {
		// Skip exited children — their pgid may already be recycled (see kill).
		p.signalIfLive("KILL")
	}
}

// buildEnv merges the caller-supplied env map over the daemon's environment.
// CLAUDE_RPC_TOKEN is stripped from the base: the reference binary does not
// propagate the auth token to spawned children (probe-verified 2026-06-09).
func buildEnv(env map[string]string) []string {
	// Block until login-shell PATH extraction has finished, so the first spawn
	// sees the same PATH as every later one. No-op once extraction is done, and
	// immediate when it was never started.
	awaitLoginPATH()
	base := removeEnvKey(os.Environ(), "CLAUDE_RPC_TOKEN")
	// The login-shell PATH is applied HERE, to the child's environment, rather
	// than being installed into the daemon's own — see loginPATH in shellenv.go.
	// Applied before the caller's env so an explicit PATH in the spawn request
	// still wins.
	if lp := currentLoginPATH(); lp != "" {
		base = replaceOrAppendEnv(base, "PATH", lp)
	}
	for k, v := range env {
		base = replaceOrAppendEnv(base, k, v)
	}
	return base
}

// removeEnvKey returns a copy of env with all entries whose key equals k removed.
func removeEnvKey(env []string, k string) []string {
	prefix := k + "="
	out := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}

func replaceOrAppendEnv(env []string, key, val string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			env[i] = prefix + val
			return env
		}
	}
	return append(env, prefix+val)
}
