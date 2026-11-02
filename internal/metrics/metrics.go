package metrics

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// Metrics holds the daemon's lifetime counters. Every field is atomic, so it can
// be bumped from any goroutine without locking; counting is always on and costs
// a single atomic add. Exposure is opt-in — nothing is served and no listener
// exists unless -metrics-addr is set (see StartServer).
type Metrics struct {
	Connections  atomic.Int64 // client connections accepted
	Spawns       atomic.Int64 // process.spawn successes
	ProcessExits atomic.Int64 // managed processes that have exited
	Reattaches   atomic.Int64 // process.reattach calls that found their process
	StreamBytes  atomic.Int64 // stdout/stderr bytes streamed to clients
	StdinBytes   atomic.Int64 // bytes accepted for child stdin
}

// Met is the process-wide metrics registry.
var Met Metrics

// writePrometheus renders the counters in the Prometheus text exposition format
// (https://prometheus.io/docs/instrumenting/exposition_formats/).
func (m *Metrics) writePrometheus(w io.Writer) {
	for _, c := range []struct {
		name, help string
		val        int64
	}{
		{"claustrum_connections_total", "Client connections accepted.", m.Connections.Load()},
		{"claustrum_process_spawns_total", "Processes started via process.spawn.", m.Spawns.Load()},
		{"claustrum_process_exits_total", "Managed processes that have exited.", m.ProcessExits.Load()},
		{"claustrum_reattaches_total", "process.reattach calls that found their process.", m.Reattaches.Load()},
		{"claustrum_stream_bytes_total", "Process stdout/stderr bytes streamed to clients.", m.StreamBytes.Load()},
		{"claustrum_stdin_bytes_total", "Bytes accepted for child stdin.", m.StdinBytes.Load()},
	} {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", c.name, c.help, c.name, c.name, c.val)
	}
}

// StartServer serves the Prometheus counters at /metrics on addr and
// returns the listener — close it to stop. It serves counts only (no command
// output, no tokens) and has no auth, so addr should be a trusted interface;
// 127.0.0.1:<port> is the intended use. It is never started unless the operator
// passes -metrics-addr, so the daemon has no inbound network surface by default.
func StartServer(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		Met.writePrometheus(w)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return ln, nil
}
