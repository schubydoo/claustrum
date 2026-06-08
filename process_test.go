package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log"
	"net"
	"strings"
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
