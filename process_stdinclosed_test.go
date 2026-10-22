package main

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// failingStdin is a child stdin whose reader is gone: each write fails.
type failingStdin struct{}

func (failingStdin) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }
func (failingStdin) Close() error              { return nil }

// TestStdinQueueFillsAfterChildClosedStdin pins cell S1 of rounds tier3-03 and
// tier3-12 (5fd08069, macOS and Linux VMs). After a write fails, process.stdin
// keeps queuing bytes and applied rises. Once the queue is full the answer is
// backpressure, and applied stays.
func TestStdinQueueFillsAfterChildClosedStdin(t *testing.T) {
	old := stdinQueueCap
	stdinQueueCap = 8
	defer func() { stdinQueueCap = old }()
	buf := captureLogBuf(t)

	p := &managedProc{id: "sclosed", subs: map[*conn]struct{}{}, stdin: failingStdin{}}
	p.stdinCond = sync.NewCond(&p.stdinMu)
	go p.stdinWriter()
	stopStdinWriter(t, p)
	live := func() bool { return true }

	if applied, _, _, full, _ := p.applyStdin([]byte("X"), nil, live); applied != 1 || full {
		t.Fatalf("first write: applied %d, full %v, want 1 and false", applied, full)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(buf.String(), "drainStdin sclosed: write error") {
		if time.Now().After(deadline) {
			t.Fatalf("no write error line, log %q", buf.String())
		}
		time.Sleep(2 * time.Millisecond)
	}

	want := []struct {
		applied uint64
		full    bool
	}{{5, false}, {9, false}, {9, true}}
	for i, w := range want {
		applied, _, _, full, _ := p.applyStdin([]byte("abcd"), nil, live)
		if applied != w.applied || full != w.full {
			t.Errorf("write %d: applied %d, full %v, want %d and %v", i+2, applied, full, w.applied, w.full)
		}
	}
	// Empty requests after the failed write queue nothing.
	p.stdinMu.Lock()
	chunks := len(p.stdinQ)
	p.stdinMu.Unlock()
	for range 5 {
		if applied, _, _, full, _ := p.applyStdin(nil, nil, live); applied != 9 || full {
			t.Errorf("empty write: applied %d, full %v, want 9 and false", applied, full)
		}
	}
	p.stdinMu.Lock()
	if len(p.stdinQ) != chunks {
		t.Errorf("queue chunks after empty writes = %d, want %d", len(p.stdinQ), chunks)
	}
	p.stdinMu.Unlock()
	if n := strings.Count(buf.String(), "drainStdin sclosed: write error"); n != 1 {
		t.Errorf("write error lines = %d, want 1", n)
	}
}
