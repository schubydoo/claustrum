package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// pipeConn returns a *conn whose writes are decoded into frames channel. It
// backs the conn with net.Pipe and drains the read side in a goroutine so
// synchronous writes (replay) don't deadlock.
func pipeConn(t *testing.T) (*conn, <-chan streamFrame) {
	t.Helper()
	client, server := net.Pipe()
	frames := make(chan streamFrame, 64)
	go func() {
		sc := bufio.NewScanner(server)
		for sc.Scan() {
			var f streamFrame
			if err := json.Unmarshal(sc.Bytes(), &f); err == nil {
				frames <- f
			}
		}
	}()
	t.Cleanup(func() { client.Close(); server.Close() })
	return &conn{nc: client}, frames
}

// emit assigns a monotonic per-process seq starting at 1 and stamps every frame
// as a "stream" notification carrying the process id.
func TestEmitAssignsSeqAndStamps(t *testing.T) {
	p := &managedProc{id: "p1", subs: map[*conn]struct{}{}}
	p.emit(streamFrame{Stream: "stdout", Data: "a"})
	p.emit(streamFrame{Stream: "stderr", Data: "b"})

	if len(p.buffer) != 2 {
		t.Fatalf("buffer len = %d, want 2", len(p.buffer))
	}
	for i, f := range p.buffer {
		if f.Seq != uint64(i+1) {
			t.Errorf("frame %d seq = %d, want %d", i, f.Seq, i+1)
		}
		if f.Type != "stream" {
			t.Errorf("frame %d type = %q, want stream", i, f.Type)
		}
		if f.ProcessID != "p1" {
			t.Errorf("frame %d processId = %q, want p1", i, f.ProcessID)
		}
	}
}

// emit caps the replay buffer at bufCap: old frames are dropped (and firstSeq
// advances) so the buffer never grows without bound. The most recently added
// frame is always kept even if it alone exceeds the cap.
func TestEmitBufferCap(t *testing.T) {
	// Use a per-instance cap to avoid touching global state while goroutines
	// from earlier spawn tests may still be calling emit on their own processes.
	p := &managedProc{id: "p1", subs: map[*conn]struct{}{}, bufCap: 10}
	// Each frame has Data "aaaaaaaaaa" (10 bytes). After 3 emits the buffer
	// would be 30 bytes — well over the 10-byte cap — so older frames must drop.
	for i := 0; i < 5; i++ {
		p.emit(streamFrame{Stream: "stdout", Data: "aaaaaaaaaa"})
	}
	if p.bufBytes > p.bufCap {
		t.Errorf("bufBytes = %d after cap, want ≤ %d", p.bufBytes, p.bufCap)
	}
	if len(p.buffer) == 0 {
		t.Fatal("buffer is empty — at least the last frame must be retained")
	}
	// firstSeq must have advanced past 1 (old frames were trimmed).
	if p.buffer[0].Seq <= 1 {
		t.Errorf("buffer[0].Seq = %d, want > 1 (old frames trimmed)", p.buffer[0].Seq)
	}
}

// A single frame larger than the whole cap is still retained: the trim loop
// keeps at least one frame (`len(p.buffer) > 1`), so the buffer is never left
// empty. Pins that boundary — the `>= 1` mutant would drop the sole frame,
// leaving nothing to replay on reattach.
func TestEmitKeepsSoleOverCapFrame(t *testing.T) {
	p := &managedProc{id: "p1", subs: map[*conn]struct{}{}, bufCap: 10}
	p.emit(streamFrame{Stream: "stdout", Data: strings.Repeat("x", 25)}) // 25 bytes > cap 10
	if len(p.buffer) != 1 {
		t.Fatalf("buffer len = %d, want 1 (sole over-cap frame must be kept)", len(p.buffer))
	}
	if p.buffer[0].Seq != 1 {
		t.Errorf("retained frame seq = %d, want 1", p.buffer[0].Seq)
	}
}

// Buffered bytes exactly equal to the cap trim nothing: the condition is strict
// (`p.bufBytes > cap`). Pins that boundary — the `>= cap` mutant would drop the
// oldest frame at the exact-equal point, wrongly advancing firstSeq past 1.
func TestEmitRetainsAllAtExactCap(t *testing.T) {
	p := &managedProc{id: "p1", subs: map[*conn]struct{}{}, bufCap: 10}
	p.emit(streamFrame{Stream: "stdout", Data: "aaaaa"}) // 5 bytes, seq 1
	p.emit(streamFrame{Stream: "stdout", Data: "bbbbb"}) // 5 bytes, seq 2 -> total exactly 10
	if len(p.buffer) != 2 {
		t.Fatalf("buffer len = %d, want 2 (exactly at cap keeps every frame)", len(p.buffer))
	}
	if p.buffer[0].Seq != 1 {
		t.Errorf("firstSeq = %d, want 1 (no trim when bufBytes == cap)", p.buffer[0].Seq)
	}
}

func TestReattachUnknownProcess(t *testing.T) {
	m := newProcManager()
	c, _ := pipeConn(t)
	_, found, running, first, last, _ := m.reattach(c, "missing", 0)
	if found || running || first != 0 || last != 0 {
		t.Errorf("reattach(missing) = (%v,%v,%d,%d), want all zero/false", found, running, first, last)
	}
}

// reattach replays only frames newer than fromSeq, but always reports the full
// buffer's first/last seq and the running flag.
func TestReattachReplaysFromSeq(t *testing.T) {
	m := newProcManager()
	p := &managedProc{id: "p1", subs: map[*conn]struct{}{}, running: true}
	p.emit(streamFrame{Stream: "stdout", Data: "a"}) // seq 1
	p.emit(streamFrame{Stream: "stdout", Data: "b"}) // seq 2
	p.emit(streamFrame{Stream: "stdout", Data: "c"}) // seq 3
	m.procs["p1"] = p

	c, frames := pipeConn(t)
	_, found, running, first, last, _ := m.reattach(c, "p1", 1)
	if !found || !running || first != 1 || last != 3 {
		t.Errorf("reattach = (%v,%v,%d,%d), want (true,true,1,3)", found, running, first, last)
	}

	// Only seq 2 and 3 should be replayed (seq > fromSeq=1).
	got := collect(t, frames, 2)
	if len(got) != 2 || got[0].Seq != 2 || got[1].Seq != 3 {
		t.Fatalf("replayed seqs = %v, want [2 3]", seqs(got))
	}

	// The reattached conn is now subscribed: a new emit reaches it live.
	p.emit(streamFrame{Stream: "stdout", Data: "d"}) // seq 4
	live := collect(t, frames, 1)
	if len(live) != 1 || live[0].Seq != 4 {
		t.Fatalf("live frame seqs = %v, want [4]", seqs(live))
	}
}

func collect(t *testing.T, ch <-chan streamFrame, n int) []streamFrame {
	t.Helper()
	out := make([]streamFrame, 0, n)
	deadline := time.After(2 * time.Second)
	for len(out) < n {
		select {
		case f := <-ch:
			out = append(out, f)
		case <-deadline:
			return out
		}
	}
	return out
}

func seqs(fs []streamFrame) []uint64 {
	out := make([]uint64, len(fs))
	for i, f := range fs {
		out[i] = f.Seq
	}
	return out
}

// When a subscribed conn's write fails (its socket is gone), emit detaches that
// conn from the proc's fan-out so a dead client doesn't accumulate frames.
func TestEmitDetachesOnWriteError(t *testing.T) {
	client, server := net.Pipe()
	client.Close() // writes now fail immediately
	server.Close()
	dead := &conn{nc: client}

	p := &managedProc{id: "p1", subs: map[*conn]struct{}{dead: {}}}
	p.emit(streamFrame{Stream: "stdout", Data: "x"})

	p.mu.Lock()
	_, still := p.subs[dead]
	p.mu.Unlock()
	if still {
		t.Error("emit kept a conn whose write failed; want it detached")
	}
}

// spawn emits the reference daemon's operational log lines: process started
// (with PID + command), per-stream "Starting <name> streaming", and the exit
// line. Capture the global logger to assert they fire. Receiving the exit frame
// guarantees all three have been written (the exit log precedes the exit emit).
func TestSpawnEmitsOperationalLogs(t *testing.T) {
	var buf bytes.Buffer
	oldW, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0) // assert on the message text, not the timestamp
	t.Cleanup(func() { log.SetOutput(oldW); log.SetFlags(oldFlags) })

	m := newProcManager()
	t.Cleanup(m.killAll)
	c, frames := pipeConn(t)
	echo, env := helperCommand(t, "echo")
	if _, err := m.spawn(c, "lg", echo, []string{"hi"}, "", env); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	// Wait for the exit frame so the exit log line has been emitted.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case f := <-frames:
			if f.Stream == "exit" {
				goto done
			}
		case <-deadline:
			t.Fatal("no exit frame")
		}
	}
done:
	got := buf.String()
	for _, want := range []string{
		"[process.Manager] Process lg started, PID=",
		"command=" + echo,
		"[process.Manager] Starting stdout streaming for process lg",
		"[process.Manager] Starting stderr streaming for process lg",
		"[process.Manager] Process lg exited with code 0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing log line %q\n--- captured ---\n%s", want, got)
		}
	}
	// confineProcess succeeded, so the failure warn must NOT fire (a negated
	// err guard would log it on every spawn).
	if strings.Contains(got, "confinement failed") {
		t.Errorf("spurious confinement-failed warn on a successful spawn\n--- captured ---\n%s", got)
	}
}

// writeResponse logs the reference's writeResponse/Failed-to-write lines when the
// underlying write fails (the client dropped the connection mid-reply).
func TestWriteResponseLogsOnWriteError(t *testing.T) {
	var buf bytes.Buffer
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldW); log.SetFlags(oldF) })

	client, server := net.Pipe()
	client.Close() // writes now fail
	server.Close()
	c := &conn{nc: client}

	c.writeResponse(pongResult{Pong: true})

	got := buf.String()
	if !strings.Contains(got, "[Server] writeResponse: wrote") ||
		!strings.Contains(got, "[Server] Failed to write response") {
		t.Errorf("writeResponse on a dead conn logged %q, want both reference lines", got)
	}
}

// reattach detaches a conn (and logs the replay-write-failed line) when replaying
// the buffer to it fails because its socket is already gone.
func TestReattachDetachesOnReplayWriteError(t *testing.T) {
	var buf bytes.Buffer
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldW); log.SetFlags(oldF) })

	m := newProcManager()
	p := &managedProc{id: "p", subs: map[*conn]struct{}{}, running: true}
	p.emit(streamFrame{Stream: "stdout", Data: "a"}) // buffered seq 1
	m.procs["p"] = p

	client, server := net.Pipe()
	client.Close()
	server.Close()
	dead := &conn{nc: client}

	_, found, _, _, _, _ := m.reattach(dead, "p", 0) // replays seq 1 → write fails → detach
	if !found {
		t.Fatal("reattach should find p")
	}
	p.mu.Lock()
	_, still := p.subs[dead]
	p.mu.Unlock()
	if still {
		t.Error("reattach kept a conn whose replay write failed; want it detached")
	}
	if !strings.Contains(buf.String(), "[frameSink] replay write failed, detaching") {
		t.Errorf("missing replay-write-failed log line; got %q", buf.String())
	}
}

// writeResponse's two remaining branches: an unmarshalable value logs
// "Failed to write response" without attempting a write, and a closed conn
// returns early (no write, no panic).
func TestWriteResponseMarshalErrorAndClosed(t *testing.T) {
	var buf bytes.Buffer
	oldW := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldW) })

	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })

	// json.Marshal fails on a channel → the marshal-error branch, no write.
	(&conn{nc: client}).writeResponse(map[string]any{"bad": make(chan int)})
	if !strings.Contains(buf.String(), "[Server] Failed to write response") {
		t.Errorf("marshal error not logged: %q", buf.String())
	}
	// A closed conn returns before writing (would otherwise block on the pipe).
	(&conn{nc: client, closed: true}).writeResponse(pongResult{Pong: true})
}

// firstStdout drains frames until the first non-empty stdout frame and returns
// its decoded, space-trimmed payload.
func firstStdout(t *testing.T, ch <-chan streamFrame) string {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case f := <-ch:
			if f.Stream == "stdout" && f.Data != "" {
				b, err := base64.StdEncoding.DecodeString(f.Data)
				if err != nil {
					t.Fatalf("bad base64 in stdout frame: %v", err)
				}
				return strings.TrimSpace(string(b))
			}
		case <-deadline:
			t.Fatal("no stdout frame within deadline")
		}
	}
}

// spawn with a non-empty cwd must set the child's working directory. The pwd
// helper reports the physical cwd (ignoring any inherited $PWD, like
// /bin/pwd -P); comparing to the resolved temp dir pins the `cwd != ""` guard:
// a negated guard would skip cmd.Dir and the child would report the test's
// cwd instead.
func TestSpawnRespectsCwd(t *testing.T) {
	tmp := t.TempDir()
	want, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatalf("evalsymlinks: %v", err)
	}
	m := newProcManager()
	t.Cleanup(m.killAll)
	c, frames := pipeConn(t)
	pwd, env := helperCommand(t, "pwd")
	if _, err := m.spawn(c, "cwd", pwd, nil, tmp, env); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	// Canonicalize the child's report the same way as want: on Windows the
	// child's Getwd echoes the 8.3 short form the runner's TEMP uses
	// (C:\Users\RUNNER~1\...), while EvalSymlinks expands to the long path.
	if got := resolveTestRoot(t, firstStdout(t, frames)); got != want {
		t.Errorf("child cwd = %q, want %q (cwd != \"\" must set cmd.Dir)", got, want)
	}
}

// pumpStream must not emit a frame for a zero-length read: the `n > 0` guard
// suppresses the trailing EOF read. A boundary mutant (`n >= 0`) would push an
// empty-data stdout/stderr frame at EOF, which this test rejects.
func TestPumpStreamSkipsEmptyReads(t *testing.T) {
	m := newProcManager()
	t.Cleanup(m.killAll)
	c, frames := pipeConn(t)
	echo, env := helperCommand(t, "echo")
	if _, err := m.spawn(c, "em", echo, []string{"hi"}, "", env); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case f := <-frames:
			if f.Stream == "exit" {
				return
			}
			if (f.Stream == "stdout" || f.Stream == "stderr") && f.Data == "" {
				t.Fatalf("empty-data %s frame (seq %d): the n>0 guard regressed to n>=0", f.Stream, f.Seq)
			}
		case <-deadline:
			t.Fatal("no exit frame within deadline")
		}
	}
}

// writeStdin reports success (true) when the process exists and has a stdin pipe
// (the bytes are enqueued for async delivery), and failure for an unknown process.
func TestWriteStdinReturnValue(t *testing.T) {
	m := newProcManager()
	t.Cleanup(m.killAll)
	c, _ := pipeConn(t)
	cat, env := helperCommand(t, "cat")
	if _, err := m.spawn(c, "cat", cat, nil, "", env); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if !m.writeStdin("cat", []byte("hello\n")) {
		t.Error("writeStdin to a live process returned false; want true")
	}
	if m.writeStdin("nope", []byte("x")) {
		t.Error("writeStdin to an unknown process returned true; want false")
	}
}

// process.stdin is asynchronous: a write to a slow/non-reading child must not
// block the caller (the reference returns success before the child reads). Here
// the child's stdin (an io.Pipe never read) blocks the stdinWriter on its first
// write; enqueuing many more must still return promptly.
func TestStdinIsAsync(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close() // unblocks the writer's pipe Write so its goroutine exits
	p := &managedProc{id: "async", subs: map[*conn]struct{}{}, stdin: pw}
	p.stdinCond = sync.NewCond(&p.stdinMu)
	go p.stdinWriter()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 16; i++ {
			p.enqueueStdin([]byte("chunk"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("enqueueStdin blocked while the child wasn't reading — stdin is not async")
	}
}

// With a tiny queue cap, a producer that outruns the (blocked) writer must hit
// backpressure — and draining the child's stdin must relieve it. Pins the bounded
// queue so a non-reading child can't grow daemon memory without bound.
func TestStdinBackpressure(t *testing.T) {
	old := stdinQueueCap
	stdinQueueCap = 8
	defer func() { stdinQueueCap = old }()

	pr, pw := io.Pipe()
	defer pr.Close()
	p := &managedProc{id: "bp", subs: map[*conn]struct{}{}, stdin: pw}
	p.stdinCond = sync.NewCond(&p.stdinMu)
	go p.stdinWriter()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ { // 200 bytes total >> 8-byte cap → must block
			p.enqueueStdin([]byte("ab"))
		}
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("100 writes never blocked despite an 8-byte cap — backpressure not applied")
	case <-time.After(300 * time.Millisecond):
		// expected: the producer is parked on backpressure
	}

	// While parked, the queue must not have grown past the cap: the gate is
	// qBytes+len(data) > cap (an arithmetic mutant qBytes-len admits over-cap
	// growth before parking).
	p.stdinMu.Lock()
	qb := p.stdinQBytes
	p.stdinMu.Unlock()
	if qb > stdinQueueCap {
		t.Fatalf("queued bytes = %d while parked, want <= cap %d", qb, stdinQueueCap)
	}

	go func() { _, _ = io.Copy(io.Discard, pr) }() // drain the child stdin
	select {
	case <-done:
		// expected: draining relieved the backpressure and the producer finished
	case <-time.After(2 * time.Second):
		t.Fatal("draining the child stdin did not relieve backpressure")
	}
}

// A single write larger than the whole queue cap must be accepted immediately
// when the queue is empty: the `stdinQBytes > 0` conjunct exempts it from
// backpressure, so it can never deadlock waiting for a drain that cannot
// start. A boundary mutant (`>= 0`) would park this enqueue forever.
func TestStdinSoleOverCapWriteAccepted(t *testing.T) {
	old := stdinQueueCap
	stdinQueueCap = 8
	defer func() { stdinQueueCap = old }()

	pr, pw := io.Pipe()
	defer pr.Close()
	p := &managedProc{id: "oc", subs: map[*conn]struct{}{}, stdin: pw}
	p.stdinCond = sync.NewCond(&p.stdinMu)
	go p.stdinWriter()

	done := make(chan struct{})
	go func() { p.enqueueStdin([]byte(strings.Repeat("x", 25))); close(done) }() // 25 > cap 8
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a sole over-cap write on an empty queue blocked — the qBytes>0 exemption regressed")
	}
}

// An enqueue that fills the queue to exactly the cap must be accepted without
// blocking: the gate is strict (`qBytes+len(data) > cap`). The boundary mutant
// (`>=`) would park the producer at the exact-fit point.
func TestStdinExactCapFitAccepted(t *testing.T) {
	old := stdinQueueCap
	stdinQueueCap = 8
	defer func() { stdinQueueCap = old }()

	pr, pw := io.Pipe()
	defer pr.Close()
	p := &managedProc{id: "xf", subs: map[*conn]struct{}{}, stdin: pw}
	p.stdinCond = sync.NewCond(&p.stdinMu)
	go p.stdinWriter()

	// First chunk: the writer dequeues it and parks inside pw.Write (pr is
	// never read), leaving the queue empty but the writer busy.
	p.enqueueStdin([]byte("aaaa"))
	waitStdinQueueEmpty(t, p)

	p.enqueueStdin([]byte("bb")) // queued: 2 bytes (writer is parked mid-Write)
	done := make(chan struct{})
	go func() { p.enqueueStdin([]byte("cccccc")); close(done) }() // 2+6 == cap exactly
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("an exact-fit enqueue (qBytes+len == cap) blocked — the strict > gate regressed to >=")
	}
}

// waitStdinQueueEmpty polls until the writer has dequeued everything (it then
// sits parked inside the pipe Write, outside the lock).
func waitStdinQueueEmpty(t *testing.T, p *managedProc) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		p.stdinMu.Lock()
		empty := p.stdinQBytes == 0
		p.stdinMu.Unlock()
		if empty {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("stdin queue never drained to the parked writer")
}

// reattach to a process whose buffer is empty must report firstSeq/lastSeq 0
// without panicking. Pins the `len(p.buffer) > 0` guard: a boundary mutant
// (`len >= 0`) would index p.buffer[0] on an empty slice and panic.
func TestReattachEmptyBuffer(t *testing.T) {
	m := newProcManager()
	p := &managedProc{id: "eb", subs: map[*conn]struct{}{}, running: true} // no emits → empty buffer
	m.procs["eb"] = p
	c, _ := pipeConn(t)
	_, found, running, first, last, _ := m.reattach(c, "eb", 0)
	if !found || !running || first != 0 || last != 0 {
		t.Errorf("reattach(empty buffer) = (%v,%v,%d,%d), want (true,true,0,0)", found, running, first, last)
	}
}

// killAll must signal a live managed process so it terminates. Pins the
// `p.cmd != nil && p.cmd.Process != nil` guard: a negated sub-condition would
// skip the signal and the process would outlive the timeout (no exit frame).
func TestKillAllTerminatesLiveProcess(t *testing.T) {
	m := newProcManager()
	c, frames := pipeConn(t)
	sleep, env := helperCommand(t, "sleep")
	if _, err := m.spawn(c, "sl", sleep, []string{"60"}, "", env); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	m.killAll()
	deadline := time.After(4 * time.Second)
	for {
		select {
		case f := <-frames:
			if f.Stream == "exit" {
				return // signalled → process exited
			}
		case <-deadline:
			t.Fatal("killAll did not terminate the live process (no exit frame)")
		}
	}
}

// process.spawn with an already-live id replaces the registry entry (both spawns
// succeed, matching the reference) AND tears down the now-orphaned old process
// rather than leaking it — a claustrum divergence (OS-level, no wire change,
// IMPROVEMENTS #17).
func TestSpawnDuplicateIDReplacesAndKillsOld(t *testing.T) {
	m := newProcManager()
	t.Cleanup(m.killAll)

	c1, _ := pipeConn(t)
	sleep, env := helperCommand(t, "sleep")
	if _, err := m.spawn(c1, "dup", sleep, []string{"60"}, "", env); err != nil {
		t.Fatalf("first spawn: %v", err)
	}
	old := m.get("dup")
	if old == nil {
		t.Fatal("first spawn was not registered")
	}

	c2, _ := pipeConn(t)
	if _, err := m.spawn(c2, "dup", sleep, []string{"60"}, "", env); err != nil {
		t.Fatalf("second spawn with duplicate id: %v (both spawns must succeed)", err)
	}

	if neu := m.get("dup"); neu == old {
		t.Fatal("duplicate-id spawn did not replace the registry entry")
	} else if !neu.isRunning() {
		t.Error("replacement process should be running")
	}

	// The orphaned first process must be torn down, not left running.
	deadline := time.After(4 * time.Second)
	for old.isRunning() {
		select {
		case <-deadline:
			t.Fatal("old process was not killed on duplicate-id replace (still running)")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// writeJSON short-circuits with net.ErrClosed once the conn is marked closed,
// rather than writing to a torn-down socket.
func TestWriteJSONClosedConn(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	c := &conn{nc: client, closed: true}
	if err := c.writeJSON(pongResult{Pong: true}); err != net.ErrClosed {
		t.Errorf("writeJSON on closed conn = %v, want net.ErrClosed", err)
	}
}

// writeLine (the pre-marshaled fan-out path used by emit) honors the same
// closed-conn short-circuit as writeJSON.
func TestWriteLineClosedConn(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	c := &conn{nc: client, closed: true}
	if err := c.writeLine([]byte("{}\n")); err != net.ErrClosed {
		t.Errorf("writeLine on closed conn = %v, want net.ErrClosed", err)
	}
}

// emit marshals each frame once and fans the identical bytes out to every
// attached subscriber (the marshal-once path): with two live conns subscribed,
// both must receive the same complete frame.
func TestEmitFansOutToAllSubscribers(t *testing.T) {
	c1, f1 := pipeConn(t)
	c2, f2 := pipeConn(t)
	p := &managedProc{id: "fan", subs: map[*conn]struct{}{c1: {}, c2: {}}}
	p.emit(streamFrame{Stream: "stdout", Data: "abc"})

	for i, ch := range []<-chan streamFrame{f1, f2} {
		got := collect(t, ch, 1)
		if len(got) != 1 {
			t.Fatalf("subscriber %d received %d frames, want 1", i+1, len(got))
		}
		f := got[0]
		if f.Type != "stream" || f.ProcessID != "fan" || f.Stream != "stdout" ||
			f.Seq != 1 || f.Data != "abc" {
			t.Errorf("subscriber %d frame = %+v, want the same stream/fan/stdout/1/abc frame", i+1, f)
		}
	}
}

// One dead subscriber in the fan-out must not block or drop delivery to the
// live ones: the live conn still receives the frame, the dead conn is
// detached, and the live conn stays subscribed.
func TestEmitFanOutSurvivesDeadSubscriber(t *testing.T) {
	live, frames := pipeConn(t)
	dc, ds := net.Pipe()
	dc.Close() // writes now fail immediately
	ds.Close()
	dead := &conn{nc: dc}

	p := &managedProc{id: "fan2", subs: map[*conn]struct{}{live: {}, dead: {}}}
	p.emit(streamFrame{Stream: "stdout", Data: "x"})

	got := collect(t, frames, 1)
	if len(got) != 1 || got[0].Data != "x" {
		t.Fatalf("live subscriber got %v, want the emitted frame", got)
	}
	p.mu.Lock()
	_, deadStill := p.subs[dead]
	_, liveStill := p.subs[live]
	p.mu.Unlock()
	if deadStill {
		t.Error("dead subscriber was not detached")
	}
	if !liveStill {
		t.Error("live subscriber was wrongly detached")
	}
}

// The metrics counters tick on real process operations: spawn, stdin accept,
// stdout streaming, reattach, and exit. Asserted as deltas against the
// process-wide registry (counting is always-on); exits and stream bytes use ≥
// because exit goroutines from earlier tests may still be landing.
func TestMetricsCountProcessOps(t *testing.T) {
	spawns0 := met.spawns.Load()
	exits0 := met.processExits.Load()
	stream0 := met.streamBytes.Load()
	stdin0 := met.stdinBytes.Load()
	reatt0 := met.reattaches.Load()

	m := newProcManager()
	t.Cleanup(m.killAll)
	c, frames := pipeConn(t)
	cat, env := helperCommand(t, "cat")
	if _, err := m.spawn(c, "mc", cat, nil, "", env); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if !m.writeStdin("mc", []byte("hello\n")) {
		t.Fatal("writeStdin to a live process returned false")
	}
	if got := firstStdout(t, frames); got != "hello" {
		t.Fatalf("echoed stdout = %q, want hello", got)
	}

	c2, _ := pipeConn(t)
	if _, found, _, _, _, _ := m.reattach(c2, "mc", 0); !found {
		t.Fatal("reattach did not find the live process")
	}
	m.reattach(c2, "missing", 0) // not found → must not count

	m.kill("mc", "KILL")
	deadline := time.After(4 * time.Second)
	for exited := false; !exited; {
		select {
		case f := <-frames:
			exited = f.Stream == "exit"
		case <-deadline:
			t.Fatal("no exit frame after kill")
		}
	}

	if d := met.spawns.Load() - spawns0; d != 1 {
		t.Errorf("spawns delta = %d, want 1", d)
	}
	if d := met.stdinBytes.Load() - stdin0; d != 6 {
		t.Errorf("stdinBytes delta = %d, want 6 (len of hello\\n)", d)
	}
	if d := met.streamBytes.Load() - stream0; d < 6 {
		t.Errorf("streamBytes delta = %d, want ≥ 6 (the echoed hello\\n)", d)
	}
	if d := met.reattaches.Load() - reatt0; d != 1 {
		t.Errorf("reattaches delta = %d, want 1 (a missing-process reattach must not count)", d)
	}
	if d := met.processExits.Load() - exits0; d < 1 {
		t.Errorf("processExits delta = %d, want ≥ 1", d)
	}
}

// TestDefaultBufferCapMatchesReference pins the replay-buffer bound to the
// reference's value. This is parity, not tuning: firstSeq is wire-visible via
// process.reattach, so the cap decides which seq a reconnecting client can still
// replay from. Driving 70 MiB of output through both daemons showed the
// reference retaining ~15.85 MiB (firstSeq 7599) against claustrum's ~49.99 MiB
// (firstSeq 4133) — same accounting, different constant. Re-measure before
// changing this number.
func TestDefaultBufferCapMatchesReference(t *testing.T) {
	const want int64 = 16 * 1024 * 1024
	if defaultBufferCap != want {
		t.Errorf("defaultBufferCap = %d, want %d (the reference's 16 MiB)", defaultBufferCap, want)
	}
}

// TestEmitUsesDefaultBufferCap checks the constant is actually WIRED, not just
// declared: a proc with no per-instance bufCap override must trim against
// defaultBufferCap. TestEmitBufferCap above uses a 10-byte override, so it would
// still pass if emit's `cap == 0` fallback were broken.
func TestEmitUsesDefaultBufferCap(t *testing.T) {
	const half = 9 << 20 // two of these exceed the 16 MiB cap, one does not
	p := &managedProc{id: "defcap", subs: map[*conn]struct{}{}}
	if p.bufCap != 0 {
		t.Fatalf("bufCap = %d, want 0 so the default applies", p.bufCap)
	}
	p.emit(streamFrame{Stream: "stdout", Data: strings.Repeat("a", half)})
	if len(p.buffer) != 1 || p.buffer[0].Seq != 1 {
		t.Fatalf("after one frame: %d frames, first seq %d; want 1 frame at seq 1",
			len(p.buffer), p.buffer[0].Seq)
	}
	p.emit(streamFrame{Stream: "stdout", Data: strings.Repeat("b", half)})
	if len(p.buffer) != 1 {
		t.Errorf("after 18 MiB over a 16 MiB cap: %d frames retained, want 1", len(p.buffer))
	}
	if p.buffer[0].Seq != 2 {
		t.Errorf("buffer[0].Seq = %d, want 2 (the older frame must be dropped)", p.buffer[0].Seq)
	}
	if p.bufBytes > defaultBufferCap {
		t.Errorf("bufBytes = %d, want <= %d", p.bufBytes, defaultBufferCap)
	}
}
