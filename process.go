package main

import (
	"encoding/base64"
	"io"
	"log"
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

type managedProc struct {
	id      string
	mu      sync.Mutex
	seq     int
	buffer  []streamFrame
	subs    map[*conn]struct{}
	running bool
	stdin   io.WriteCloser
	cmd     *exec.Cmd
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
// every attached client. The buffer retains all frames for later reattach.
func (p *managedProc) emit(f streamFrame) {
	p.mu.Lock()
	p.seq++
	f.Seq = p.seq
	f.Type = "stream"
	f.ProcessID = p.id
	p.buffer = append(p.buffer, f)
	subs := make([]*conn, 0, len(p.subs))
	for c := range p.subs {
		subs = append(subs, c)
	}
	p.mu.Unlock()
	for _, c := range subs {
		if err := c.writeJSON(f); err != nil {
			log.Printf("[frameSink] write failed, detaching: %v", err)
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
		log.Printf("[process.Manager] Failed to start process %s: %v", id, err)
		return err
	}
	log.Printf("[process.Manager] Process %s started, PID=%d, command=%s", id, cmd.Process.Pid, command)

	p := &managedProc{
		id:      id,
		subs:    map[*conn]struct{}{c: {}},
		running: true,
		stdin:   stdin,
		cmd:     cmd,
	}
	m.mu.Lock()
	m.procs[id] = p
	m.mu.Unlock()

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
		log.Printf("[process.Manager] Process %s exited with code %d", id, code)
		p.emit(streamFrame{Stream: "exit", ExitCode: &code})
	}()
	return nil
}

func pumpStream(p *managedProc, name string, r io.Reader) {
	log.Printf("[process.Manager] Starting %s streaming for process %s", name, p.id)
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

func (m *procManager) writeStdin(id string, data []byte) bool {
	p := m.get(id)
	if p == nil || p.stdin == nil {
		return false
	}
	_, err := p.stdin.Write(data)
	return err == nil
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
			log.Printf("[frameSink] replay write failed, detaching: %v", err)
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
