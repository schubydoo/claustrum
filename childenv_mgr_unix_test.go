//go:build linux || darwin

package main

import (
	"path/filepath"
	"syscall"
	"testing"

	"github.com/schubydoo/claustrum/internal/childrec"
)

// TestReapOrphansOnAnotherSocketShape pins that the reap of a start runs on a socket
// that is not run/<id>/rpc.sock (`89cb6289`, measured on Linux and macOS). The run
// dir is the folder that execChildRunDir gives for such a socket.
func TestReapOrphansOnAnotherSocketShape(t *testing.T) {
	if node, _ := ownReapIdentity(); node == "" {
		t.Skip("no node identity on this host")
	}
	runDir := execChildRunDir(filepath.Join(t.TempDir(), "s.sock"))
	if runDir == "" {
		t.Fatal("no run dir for a socket of another shape")
	}
	const pid = 800000101
	writeOrphanRec(t, runDir, pid, "/f/bin/sigstub")
	sim := newOrphanSim(t, pid, "/f/bin/sigstub", runDir, syscall.SIGTERM)

	(&childrec.Records{RunDir: runDir, InstanceID: "inst-self"}).ReapOrphans()

	if sim.sigCount(syscall.SIGTERM) != 1 || !sim.signaled(pid) {
		t.Errorf("kills = %v, want one SIGTERM to group %d", sim.kills(), pid)
	}
}
