package main

import (
	"net"

	"github.com/schubydoo/claustrum/internal/metrics"
)

// The names of internal/metrics under the names that the code of package main
// uses.

// met is the process-wide registry of internal/metrics.
var met = &metrics.Met

func startMetricsServer(addr string) (net.Listener, error) { return metrics.StartServer(addr) }
