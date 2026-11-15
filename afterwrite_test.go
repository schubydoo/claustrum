package main

import (
	"bytes"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/conn"
	"github.com/schubydoo/claustrum/internal/rpc"
)

// The step after the write of a reply (rpc.Response.AfterWrite) runs after the
// frame went out. The step waits for the client to get the frame: if it ran
// first, the frame would come only after the wait, and the step would see no
// frame. The seam is written once, before the server starts (see
// TestHandlerPanicIsRecovered).
func TestAfterWriteRunsAfterTheFrame(t *testing.T) {
	got := make(chan struct{})
	ran := make(chan bool, 1)
	old := dispatchRequest
	dispatchRequest = func(s *server, c *conn.Conn, raw []byte, req request) *rpc.Response {
		resp := s.route(c, req)
		if bytes.Contains(raw, []byte(`"id":41`)) {
			resp.AfterWrite = func() {
				select {
				case <-got:
					ran <- true
				case <-time.After(2 * time.Second):
					ran <- false
				}
			}
		}
		return resp
	}
	t.Cleanup(func() { dispatchRequest = old })

	sock := startSocketServer(t)
	reply := sendRaw(t, sock, `{"jsonrpc":"2.0","id":41,"method":"server.ping","auth":"`+testToken+`"}`)
	close(got)
	if !strings.Contains(reply, `"pong":true`) {
		t.Fatalf("reply = %q, want a pong", reply)
	}
	select {
	case after := <-ran:
		if !after {
			t.Error("the step after the write ran before the client got the frame")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the step after the write did not run")
	}
}

// A step whose write has not ended runs in runPendingAfterWrites, and only once
// in all.
func TestPendingAfterWriteRunsOnce(t *testing.T) {
	var n atomic.Int32
	s := &server{}
	a := s.holdAfterWrite(func() { n.Add(1) })
	s.runPendingAfterWrites()
	if got := n.Load(); got != 1 {
		t.Fatalf("the pending step ran %d times, want 1", got)
	}
	s.runAfterWrite(a)
	s.runPendingAfterWrites()
	if got := n.Load(); got != 1 {
		t.Errorf("the step ran %d times in all, want 1", got)
	}
	if s.holdAfterWrite(nil) != nil {
		t.Error("holdAfterWrite(nil) is not nil")
	}
}
