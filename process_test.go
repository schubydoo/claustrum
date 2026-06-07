package main

import (
	"bufio"
	"encoding/json"
	"net"
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
