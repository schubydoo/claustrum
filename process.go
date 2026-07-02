package main

import (
	"encoding/base64"
	"encoding/json"
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
	Seq       int    `json:"seq"`
	Data      string `json:"data,omitempty"`
	ExitCode  *int   `json:"exitCode,omitempty"`
}

// defaultBufferCap caps the total base64-encoded data held in each per-process
// replay buffer. A long-running, high-throughput process would otherwise grow
// the buffer without bound for the daemon's entire lifetime (the buffer is never
// reclaimed while the procManager holds the entry). 50 MB keeps reattach useful
// while bounding the worst-case daemon RSS.
const defaultBufferCap int64 = 50 * 1024 * 1024

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
	seq       int
	buffer    []streamFrame
	bufBytes  int64 // sum of len(f.Data) for frames currently in buffer
	bufCap    int64 // per-instance override; 0 means use defaultBufferCap
	subs      map[*conn]struct{}
	running   bool
	stdin     io.WriteCloser
	cmd       *exec.Cmd
	group     *procGroup // OS handle for whole-tree teardown (Job Object on Windows)
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
	stdinApplied int
}

type procManager struct {
	mu    sync.Mutex
	procs map[string]*managedProc
}

func newProcManager() *procManager {
	return &procManager{procs: make(map[string]*managedProc)}
}

func (m *procManager) get(id string) *managedProc {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.procs[id]
}

// isRunning reports whether the child is still alive (the exit goroutine clears
// this under p.mu once cmd.Wait returns).
func (p *managedProc) isRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

// emit assigns the next per-process seq, buffers the frame, and fans it out to
// every attached client. The buffer retains frames for later reattach, capped
// at maxBufferBytes: the oldest frames are dropped (and firstSeq advances) when
// the cap is exceeded, matching the reattach contract on the wire.
func (p *managedProc) emit(f streamFrame) {
	p.mu.Lock()
	p.seq++
	f.Seq = p.seq
	f.Type = "stream"
	f.ProcessID = p.id
	p.buffer = append(p.buffer, f)
	p.bufBytes += int64(len(f.Data))
	cap := p.bufCap
	if cap == 0 {
		cap = defaultBufferCap
	}
	// Trim oldest frames while over cap, keeping at least the frame just added.
	for len(p.buffer) > 1 && p.bufBytes > cap {
		p.bufBytes -= int64(len(p.buffer[0].Data))
		p.buffer = p.buffer[1:]
	}
	subs := make([]*conn, 0, len(p.subs))
	for c := range p.subs {
		subs = append(subs, c)
	}
	p.mu.Unlock()
	// Marshal once and fan the same bytes out to every subscriber — the frames
	// are identical per conn, so re-marshaling per subscriber was pure waste.
	// json.Marshal of a streamFrame cannot realistically fail (strings + ints);
	// if it somehow does, every subscriber would have failed the same way, so
	// mirror the old per-conn behavior: log + detach each.
	b, merr := json.Marshal(f)
	if merr == nil {
		b = append(b, '\n')
	}
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
	cmd := exec.Command(command, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = buildEnv(env)
	cmd.SysProcAttr = newSysProcAttr()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		logErrorf("[process.Manager] Failed to start process %s: %v", id, err)
		return nil, err
	}
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
		if old.cmd != nil && old.cmd.Process != nil && old.isRunning() {
			old.group.signal(old.cmd.Process, "KILL")
		}
	}
	m.procs[id] = p
	m.mu.Unlock()

	go p.stdinWriter()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); pumpStream(p, "stdout", stdout) }()
	go func() { defer wg.Done(); pumpStream(p, "stderr", stderr) }()
	go func() {
		wg.Wait()
		err := cmd.Wait()
		code := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode() // -1 when terminated by signal
			} else {
				code = -1
			}
		}
		p.mu.Lock()
		p.running = false
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
		logInfof("[process.Manager] Process %s exited with code %d", id, code)
		met.processExits.Add(1)
		p.emit(streamFrame{Stream: "exit", ExitCode: &code})
		// Signal any killAndWait waiter now the child is fully reaped.
		close(p.done)
	}()
	return p, nil
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
func (p *managedProc) applyStdin(data []byte, offset *int) (applied int, duplicate, gap bool) {
	p.stdinMu.Lock()
	cur := p.stdinApplied
	if offset != nil {
		off := *offset
		if off > cur {
			p.stdinMu.Unlock()
			return cur, false, true
		}
		if skip := cur - off; skip > 0 {
			if skip >= len(data) {
				p.stdinMu.Unlock()
				return cur, true, false
			}
			data = data[skip:]
		}
	}
	p.enqueueStdinLocked(data)
	p.stdinApplied += len(data)
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
	if p == nil || p.cmd == nil || p.cmd.Process == nil || !p.isRunning() {
		return
	}
	p.group.signal(p.cmd.Process, signal)
}

// defaultKillWaitMs is the graceful-signal grace killAndWait uses when the caller
// sends no timeoutMs (or a non-positive one). Probe-measured at 3000ms against the
// reference. var so tests can shrink it. maxKillWaitMs caps an absurd caller value
// so a signal-ignoring child + escalate:false can't wedge the dispatch goroutine
// indefinitely — the reference clamps too (clampKillWaitMs); its exact ceiling is
// above the ~90s we could observe, so this generous cap only bites on adversarial
// input, never a real client.
var defaultKillWaitMs = 3000

const maxKillWaitMs = 600000 // 10 min

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
	if p.cmd != nil && p.cmd.Process != nil {
		p.group.signal(p.cmd.Process, signal)
	}
	select {
	case <-p.done:
		return true, true, false, false
	case <-time.After(grace):
	}
	// Still alive after the grace window. With escalate:false the caller wants a
	// best-effort graceful kill only — leave the process running and report it.
	if !escalate {
		return true, false, false, false
	}
	// The graceful signal was ignored — force-kill the group and wait for the reap.
	if p.cmd != nil && p.cmd.Process != nil {
		p.group.signal(p.cmd.Process, "KILL")
	}
	select {
	case <-p.done:
		return true, true, false, true
	case <-time.After(grace):
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
func (m *procManager) reattach(c *conn, id string, fromSeq int) (p *managedProc, found, running bool, firstSeq, lastSeq, stdinApplied int) {
	p = m.get(id)
	if p == nil {
		return nil, false, false, 0, 0, 0
	}
	met.reattaches.Add(1)
	p.mu.Lock()
	p.subs[c] = struct{}{}
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
		if p.cmd != nil && p.cmd.Process != nil && p.isRunning() {
			p.group.signal(p.cmd.Process, "KILL")
		}
	}
}

// buildEnv merges the caller-supplied env map over the daemon's environment.
// CLAUDE_RPC_TOKEN is stripped from the base: the reference binary does not
// propagate the auth token to spawned children (probe-verified 2026-06-09).
func buildEnv(env map[string]string) []string {
	base := removeEnvKey(os.Environ(), "CLAUDE_RPC_TOKEN")
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
