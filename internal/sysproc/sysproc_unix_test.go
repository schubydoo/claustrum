//go:build unix

package sysproc

import (
	"bytes"
	"log"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// ReapProcessGroup is nil-safe: a command that never started has
// a nil cmd.Process, and reading its Pid for the pgid would panic inside the drain
// path, so the guard must come first.
func TestReapProcessGroupNilIsNoOp(t *testing.T) {
	ReapProcessGroup(nil)
}

// noSuchPid is the pgid of the fake group behind the groupSignal seam. It is above the pid
// limit of Linux and macOS, so it names no group.
const noSuchPid = 1 << 30

// TestGroupSignalRefusesASmallPid: KillChildGroup returns an error for pgid 0 and pgid 1,
// ReapProcessGroup sends nothing for them, and neither reaches the groupSignal seam. Each
// refusal logs one line.
func TestGroupSignalRefusesASmallPid(t *testing.T) {
	old := groupSignal
	t.Cleanup(func() { groupSignal = old })
	var calls []int
	groupSignal = func(pgid int, _ syscall.Signal) error { calls = append(calls, pgid); return nil }
	var buf bytes.Buffer
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldW); log.SetFlags(oldF) })

	for _, pgid := range []int{0, 1} {
		if err := KillChildGroup(pgid); err == nil {
			t.Errorf("KillChildGroup(%d) = nil, want an error", pgid)
		}
		ReapProcessGroup(&os.Process{Pid: pgid})
	}
	if len(calls) != 0 {
		t.Fatalf("seam calls = %v, want none", calls)
	}
	for _, pgid := range []string{"0", "1"} {
		line := "[signal] refused a signal to pid " + pgid + ": a pid below 2 is never a child\n"
		if got := strings.Count(buf.String(), line); got != 2 {
			t.Errorf("log has %d lines %q, want 2", got, line)
		}
	}
	for _, pgid := range []int{2, noSuchPid} {
		if err := KillChildGroup(pgid); err != nil {
			t.Errorf("KillChildGroup(%d) = %v, want nil", pgid, err)
		}
		ReapProcessGroup(&os.Process{Pid: pgid})
	}
	if want := []int{2, 2, noSuchPid, noSuchPid}; !slices.Equal(calls, want) {
		t.Errorf("seam calls = %v, want %v", calls, want)
	}
}
