package main

import (
	"encoding/base64"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
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
	id       string
	mu       sync.Mutex
	seq      int
	buffer   []streamFrame
	bufBytes int64 // sum of len(f.Data) for frames currently in buffer
	bufCap   int64 // per-instance override; 0 means use defaultBufferCap
	subs     map[*conn]struct{}
	running  bool
	stdin    io.WriteCloser
	cmd      *exec.Cmd

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
	for _, c := range subs {
		if err := c.writeJSON(f); err != nil {
			logWarnf("[frameSink] write failed, detaching: %v", err)
			p.mu.Lock()
			delete(p.subs, c)
			p.mu.Unlock()
		}
	}
}

// spawn starts a child process in its own process group and begins streaming.
func (m *procManager) spawn(c *conn, id, command string, args []string, cwd string, env map[string]string) error {
	cmd := exec.Command(command, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = buildEnv(env)
	cmd.SysProcAttr = newSysProcAttr()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		logErrorf("[process.Manager] Failed to start process %s: %v", id, err)
		return err
	}
	logInfof("[process.Manager] Process %s started, PID=%d, command=%s", id, cmd.Process.Pid, command)

	p := &managedProc{
		id:      id,
		subs:    map[*conn]struct{}{c: {}},
		running: true,
		stdin:   stdin,
		cmd:     cmd,
	}
	p.stdinCond = sync.NewCond(&p.stdinMu)
	m.mu.Lock()
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
		logInfof("[process.Manager] Process %s exited with code %d", id, code)
		p.emit(streamFrame{Stream: "exit", ExitCode: &code})
	}()
	return nil
}

func pumpStream(p *managedProc, name string, r io.Reader) {
	logDebugf("[process.Manager] Starting %s streaming for process %s", name, p.id)
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
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
	defer p.stdinMu.Unlock()
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

// kill is best-effort and signals the whole process group (OS-specific).
func (m *procManager) kill(id, signal string) {
	p := m.get(id)
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	signalProcessGroup(p.cmd.Process, signal)
}

// reattach replays buffered frames with seq > fromSeq to c, (re)subscribes c for
// future frames, and reports the buffer/running state.
func (m *procManager) reattach(c *conn, id string, fromSeq int) (found, running bool, firstSeq, lastSeq int) {
	p := m.get(id)
	if p == nil {
		return false, false, 0, 0
	}
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
	for _, f := range replay {
		if err := c.writeJSON(f); err != nil {
			logWarnf("[frameSink] replay write failed, detaching: %v", err)
			p.mu.Lock()
			delete(p.subs, c)
			p.mu.Unlock()
			break
		}
	}
	return true, running, firstSeq, lastSeq
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

func (m *procManager) killAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.procs {
		if p.cmd != nil && p.cmd.Process != nil {
			signalProcessGroup(p.cmd.Process, "KILL")
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
