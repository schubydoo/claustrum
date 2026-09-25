package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// -stop must not block forever on a daemon that accepts the connection and then
// never replies — a wedged daemon, or a stale socket now owned by something
// else. Measured at 5db5e4a: the reference returns in 2.030s against a silent
// socket. claustrum was still blocked when killed at 45s.
func TestRunStopBoundsTheReplyWait(t *testing.T) {
	old := stopReplyTimeout
	stopReplyTimeout = 200 * time.Millisecond
	t.Cleanup(func() { stopReplyTimeout = old })

	// Short base path keeps the AF_UNIX path under the macOS sun_path limit.
	dir, err := os.MkdirTemp("", "stt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "rpc.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	// Accept and hold the connection open without ever writing a reply.
	held := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		held <- c
	}()
	t.Cleanup(func() {
		select {
		case c := <-held:
			_ = c.Close()
		default:
		}
	})

	done := make(chan string, 1)
	go func() {
		var out bytes.Buffer
		runStop(sock, &out)
		done <- out.String()
	}()
	select {
	case got := <-done:
		if got != "stopped\n" {
			t.Errorf("runStop printed %q after the read deadline, want %q", got, "stopped\n")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runStop blocked on a silent daemon — the read deadline is missing")
	}
}
