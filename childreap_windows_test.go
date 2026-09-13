//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReapOrphansWindowsNoop pins the verified windows contract: the startup reap ends no
// process and forgets no record on windows. Measured on a windows VM, the 19f30c46 reference
// daemon reports it is not the run-dir lock holder on windows: it leaves any predecessor's
// recorded children alone (planted records survived a daemon startup untouched). The planted
// record — foreign and long expired, so a linux/darwin reap would forget it — must remain
// after reapOrphans. A mutant that ported the reap to windows would fail here.
func TestReapOrphansWindowsNoop(t *testing.T) {
	runDir := t.TempDir()
	children := filepath.Join(runDir, "children")
	if err := os.MkdirAll(children, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := filepath.Join(children, "424242.json")
	body := `{"pid":424242,"node":"foreign-boot","host":"foreign-host","instance":"other","daemonPid":2,"daemonStart":"1","argv0":"x","start":"1","at":1}`
	if err := os.WriteFile(rec, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	reapOrphans(runDir, "our-instance")

	if _, err := os.Stat(rec); err != nil {
		t.Errorf("reapOrphans touched a planted record on windows; it must leave it alone (never the run-dir lock holder): %v", err)
	}
}
