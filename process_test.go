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
		if f.Seq != i+1 {
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
	found, running, first, last := m.reattach(c, "missing", 0)
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
	found, running, first, last := m.reattach(c, "p1", 1)
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

func seqs(fs []streamFrame) []int {
	out := make([]int, len(fs))
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
	if err := m.spawn(c, "lg", "/bin/echo", []string{"hi"}, "", nil); err != nil {
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
		"command=/bin/echo",
		"[process.Manager] Starting stdout streaming for process lg",
		"[process.Manager] Starting stderr streaming for process lg",
		"[process.Manager] Process lg exited with code 0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing log line %q\n--- captured ---\n%s", want, got)
		}
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

	found, _, _, _ := m.reattach(dead, "p", 0) // replays seq 1 → write fails → detach
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

// spawn with a non-empty cwd must set the child's working directory. Running
// /bin/pwd -P (physical cwd, ignoring any inherited $PWD) and comparing to the
// resolved temp dir pins the `cwd != ""` guard: a negated guard would skip
// cmd.Dir and the child would report the test's cwd instead.
func TestSpawnRespectsCwd(t *testing.T) {
	tmp := t.TempDir()
	want, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatalf("evalsymlinks: %v", err)
	}
	m := newProcManager()
	t.Cleanup(m.killAll)
	c, frames := pipeConn(t)
	if err := m.spawn(c, "cwd", "/bin/pwd", []string{"-P"}, tmp, nil); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if got := firstStdout(t, frames); got != want {
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
	if err := m.spawn(c, "em", "/bin/echo", []string{"hi"}, "", nil); err != nil {
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
	if err := m.spawn(c, "cat", "/bin/cat", nil, "", nil); err != nil {
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

	go func() { _, _ = io.Copy(io.Discard, pr) }() // drain the child stdin
	select {
	case <-done:
		// expected: draining relieved the backpressure and the producer finished
	case <-time.After(2 * time.Second):
		t.Fatal("draining the child stdin did not relieve backpressure")
	}
	pr.Close()
}

// reattach to a process whose buffer is empty must report firstSeq/lastSeq 0
// without panicking. Pins the `len(p.buffer) > 0` guard: a boundary mutant
// (`len >= 0`) would index p.buffer[0] on an empty slice and panic.
func TestReattachEmptyBuffer(t *testing.T) {
	m := newProcManager()
	p := &managedProc{id: "eb", subs: map[*conn]struct{}{}, running: true} // no emits → empty buffer
	m.procs["eb"] = p
	c, _ := pipeConn(t)
	found, running, first, last := m.reattach(c, "eb", 0)
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
	if err := m.spawn(c, "sl", "/bin/sleep", []string{"60"}, "", nil); err != nil {
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
