package main

import (
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// process.stdin is the one method whose ARRIVAL ORDER is part of its meaning:
// the chunks form a byte stream, and reordering them corrupts it. Everything
// else on a connection may be dispatched concurrently and reply out of order —
// that is contract — but stdin must reach the child in wire order.
//
// Measured at 5db5e4a with 20 chunks pipelined back-to-back on one connection:
// the reference delivered all 20 in order, claustrum delivered all 20 scrambled
// ("L02 L03 L00 L08 L04 …") because each request raced to the queue in its own
// goroutine.
//
// SCOPE, stated plainly: this is a SMOKE test, not the regression guard. The
// in-process harness does not reproduce the reordering — it still passes against
// the unfixed daemon, verified with bursts of 40 and 300 requests over repeated
// runs. The evidence for the bug and the fix is the real-daemon differential
// above (scrambled -> ordered, reference ordered throughout).
// TestStdinTicketGateAdmitsInOrder is the deterministic guard on the mechanism.
func TestStdinPipelinedRequestsKeepArrivalOrder(t *testing.T) {
	_, sock := newRunningServer(t)
	cl := dial(t, sock)

	const id = "ord"
	cl.call(spawnReqArgs(t, 1, id, "cat"))

	// All requests go out in ONE write, so the daemon reads them back-to-back and
	// the per-request goroutines genuinely race. Sending them one at a time lets
	// each finish before the next arrives, which hides the bug entirely — an
	// earlier version of this test passed against the unfixed daemon for exactly
	// that reason.
	const n = 40
	var want, batch strings.Builder
	for i := 0; i < n; i++ {
		line := fmt.Sprintf("L%03d\n", i)
		want.WriteString(line)
		if i > 0 {
			batch.WriteString("\n")
		}
		fmt.Fprintf(&batch,
			`{"jsonrpc":"2.0","id":%d,"method":"process.stdin","auth":%q,"params":{"id":%q,"data":%q}}`,
			100+i, testToken, id, base64.StdEncoding.EncodeToString([]byte(line)))
	}
	cl.send(batch.String())

	// cl.wait holds cl.mu while evaluating the condition, so the condition must
	// NOT take it again — cl.mu is a plain sync.Mutex and re-locking deadlocks.
	cl.wait(func() bool { return len(stdoutLocked(cl, id)) >= want.Len() })

	if got := stdoutLocked(cl, id); got != want.String() {
		t.Errorf("stdin arrived out of order.\n got: %s\nwant: %s",
			firstLines(got, 10), firstLines(want.String(), 10))
	}
}

// stdoutLocked concatenates this process's stdout. The caller must already hold
// cl.mu (cl.wait does). The child here is `cat`, which never exits while stdin
// stays open, so waitExit cannot be used.
func stdoutLocked(cl *testClient, processID string) string {
	var sb []byte
	for _, f := range cl.fr {
		if f.ProcessID != processID || f.Stream != "stdout" {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(f.Data)
		if err != nil {
			continue
		}
		sb = append(sb, b...)
	}
	return string(sb)
}

// firstLines trims a stream to its first n lines for a readable failure message.
func firstLines(s string, n int) string {
	parts := strings.SplitN(s, "\n", n+1)
	if len(parts) > n {
		parts = parts[:n]
	}
	return strings.Join(parts, " ")
}

// The ordering gate itself, deterministically. Tickets are handed out in wire
// order by the connection's read loop; holders must then be admitted strictly in
// that order no matter how their goroutines are scheduled. Each holder is
// launched in reverse order and made to wait, so an ungated implementation would
// record admissions backwards.
func TestStdinTicketGateAdmitsInOrder(t *testing.T) {
	c := &conn{}
	const n = 25

	tickets := make([]uint64, n)
	for i := range tickets {
		tickets[i] = c.nextStdinTicket() // read-loop order
	}

	var mu sync.Mutex
	var admitted []uint64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := n - 1; i >= 0; i-- { // launch in reverse to stress the gate
		wg.Add(1)
		go func(tk uint64) {
			defer wg.Done()
			<-start
			c.awaitStdinTurn(tk)
			mu.Lock()
			admitted = append(admitted, tk)
			mu.Unlock()
			c.doneStdinTurn(tk)
		}(tickets[i])
	}
	close(start)
	wg.Wait()

	for i, tk := range admitted {
		if tk != uint64(i) {
			t.Fatalf("admission order = %v, want 0..%d in order", admitted, n-1)
		}
	}
}
