package main

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

// writePrometheus must emit valid exposition format (HELP/TYPE/value triplets)
// for every counter. Uses a local metrics value, not the global, so the assert
// is deterministic regardless of what other tests have counted.
func TestMetricsPrometheusFormat(t *testing.T) {
	var m metrics
	m.connections.Store(3)
	m.spawns.Store(2)
	m.streamBytes.Store(4096)

	var buf bytes.Buffer
	m.writePrometheus(&buf)
	out := buf.String()

	for _, want := range []string{
		"# HELP claustrum_connections_total Client connections accepted.",
		"# TYPE claustrum_connections_total counter",
		"claustrum_connections_total 3",
		"claustrum_process_spawns_total 2",
		"claustrum_stream_bytes_total 4096",
		"# TYPE claustrum_reattaches_total counter",
		"claustrum_process_exits_total 0",
		"claustrum_stdin_bytes_total 0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writePrometheus missing %q\n--- got ---\n%s", want, out)
		}
	}
}

// startMetricsServer must bind, serve /metrics with the Prometheus content type,
// and stop when the returned listener is closed.
func TestMetricsServerServes(t *testing.T) {
	ln, err := startMetricsServer("127.0.0.1:0") // :0 → an ephemeral free port
	if err != nil {
		t.Fatalf("startMetricsServer: %v", err)
	}
	defer ln.Close()

	resp, err := http.Get("http://" + ln.Addr().String() + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain…", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "claustrum_connections_total") {
		t.Errorf("body missing a known counter:\n%s", body)
	}
}

// Closing the listener must stop the server (a subsequent request fails).
func TestMetricsServerStopsOnClose(t *testing.T) {
	ln, err := startMetricsServer("127.0.0.1:0")
	if err != nil {
		t.Fatalf("startMetricsServer: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if resp, err := http.Get("http://" + addr + "/metrics"); err == nil {
		resp.Body.Close()
		t.Error("expected request to fail after the listener was closed")
	}
}
