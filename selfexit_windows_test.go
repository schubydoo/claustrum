//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// In cell K3 of round tier1-01 (Windows VM) a daemon whose socket file was deleted
// wrote no line and was still alive after 14 min. The idle exit does not run on
// Windows either (not measured). With
// short timers and both exits due, exitWhenOrphaned returns at once and starts no
// shutdown.
func TestSelfExitOffOnWindows(t *testing.T) {
	oi, og, oe := orphanCheckInterval, orphanGrace, idleExitAfter
	orphanCheckInterval, orphanGrace, idleExitAfter = 5*time.Millisecond, 5*time.Millisecond, time.Millisecond
	t.Cleanup(func() { orphanCheckInterval, orphanGrace, idleExitAfter = oi, og, oe })

	dir := t.TempDir()
	marker := filepath.Join(dir, "bound")
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}
	s := &server{
		conns:      make(map[*conn]struct{}),
		shutdown:   make(chan struct{}),
		procs:      newProcManager(),
		sockInfo:   fi,
		instanceID: "0123456789abcdef0123456789abcdef",
	}
	done := make(chan struct{})
	go func() { s.exitWhenOrphaned(filepath.Join(dir, "gone.sock")); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		s.signalShutdown()
		<-done
		t.Fatal("exitWhenOrphaned ran its checks on Windows")
	}
	select {
	case <-s.shutdown:
		t.Fatal("a self-exit shut the daemon down on Windows")
	default:
	}
}
