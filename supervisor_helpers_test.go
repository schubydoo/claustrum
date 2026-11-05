package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/conn"
	"github.com/schubydoo/claustrum/internal/supervisor"
)

// The helpers of the tests of this package that drive the manager of
// internal/supervisor. The tests of internal/supervisor have their own copies.

// pipeConn builds a conn over one end of a net.Pipe and returns the stream frames
// that reach the other end.
func pipeConn(t *testing.T) (*conn.Conn, <-chan supervisor.StreamFrame) {
	t.Helper()
	client, server := net.Pipe()
	frames := make(chan supervisor.StreamFrame, 64)
	go func() {
		sc := bufio.NewScanner(server)
		for sc.Scan() {
			var f supervisor.StreamFrame
			if err := json.Unmarshal(sc.Bytes(), &f); err == nil {
				frames <- f
			}
		}
	}()
	t.Cleanup(func() { client.Close(); server.Close() })
	return conn.New(client, nil, 0, nil, nil, nil), frames
}

// firstStdout returns the decoded data of the first stdout frame, trimmed.
func firstStdout(t *testing.T, ch <-chan supervisor.StreamFrame) string {
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
