//go:build linux || darwin

package main

import (
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
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
	writeRec(t, runDir, orphanRecord(pid, "/f/bin/sigstub", ""))
	sim := newProcSim(t)
	sim.live[pid] = &liveProc{state: procAlive, startTicks: "9000", pgid: pid,
		program: "/f/bin/sigstub", runDir: runDir, childMark: strconv.Itoa(pid) + ":9000"}
	sim.diesOn(pid, syscall.SIGTERM)

	reapOrphans(runDir, "inst-self")

	if sim.sigCount(syscall.SIGTERM) != 1 || !sim.signaled(pid) {
		t.Errorf("kills = %v, want one SIGTERM to group %d", sim.kills, pid)
	}
}
