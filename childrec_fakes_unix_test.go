//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/schubydoo/claustrum/internal/childrec"
)

// The fakes of the reap of a start that the tests of this package use. They seam
// every read of a live process and every signal through internal/childrec, so no test
// here signals a real process.

// writeOrphanRec writes the record of a child pid of a gone daemon below runDir and
// returns the name of the record.
func writeOrphanRec(t *testing.T, runDir string, pid int, argv0 string) string {
	t.Helper()
	name, err := childrec.WriteOrphanRecordForTest(runDir, pid, argv0)
	if err != nil {
		t.Fatalf("write the record: %v", err)
	}
	return name
}

func recordExists(runDir, name string) bool {
	_, err := os.Stat(filepath.Join(runDir, "children", name))
	return err == nil
}

// orphanSim is the fake of one recorded child of a gone daemon (FakeOrphanForTest).
type orphanSim struct {
	signals func() []childrec.GroupSignalForTest
}

// newOrphanSim makes pid a live orphan that runs program with runDir in its
// environment, and that a group signal dieOn ends. The cleanup of t puts the seams back.
func newOrphanSim(t *testing.T, pid int, program, runDir string, dieOn syscall.Signal) *orphanSim {
	t.Helper()
	signals, restore := childrec.FakeOrphanForTest(pid, program, runDir, dieOn)
	t.Cleanup(restore)
	return &orphanSim{signals: signals}
}

// kills returns the group signals, in order.
func (s *orphanSim) kills() []childrec.GroupSignalForTest { return s.signals() }

func (s *orphanSim) sigCount(sig syscall.Signal) int {
	n := 0
	for _, k := range s.signals() {
		if k.Sig == sig {
			n++
		}
	}
	return n
}

func (s *orphanSim) signaled(pid int) bool {
	for _, k := range s.signals() {
		if k.Pid == pid {
			return true
		}
	}
	return false
}
