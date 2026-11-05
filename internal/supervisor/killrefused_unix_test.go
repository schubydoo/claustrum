//go:build unix

package supervisor

import (
	"os"
	"syscall"
	"testing"

	"github.com/schubydoo/claustrum/internal/sysproc"
)

// refusingSignals makes every signal of a child fail with err. Both seams take the
// call, so no signal goes out: sysproc.SignalOne for TERM, INT and HUP, and
// sysproc.KillChildGroup for KILL. It returns the record of the calls, "one <sig>" or "group".
func refusingSignals(t *testing.T, err error) *[]string {
	t.Helper()
	oldOne, oldGroup := sysproc.SignalOne, sysproc.KillChildGroup
	t.Cleanup(func() { sysproc.SignalOne, sysproc.KillChildGroup = oldOne, oldGroup })
	var calls []string
	sysproc.SignalOne = func(_ *os.Process, sig syscall.Signal) error {
		calls = append(calls, "one "+sig.String())
		return err
	}
	sysproc.KillChildGroup = func(pgid int) error {
		calls = append(calls, "group")
		return err
	}
	return &calls
}

// SignalProcessGroup refuses pid 0 and pid 1 with an error for every signal name, before
// either seam (claustrum's own). A real child never has such a pid.
func TestSignalProcessGroupRefusesASmallPid(t *testing.T) {
	calls := refusingSignals(t, nil)
	for _, pid := range []int{0, 1} {
		for _, name := range []string{"TERM", "KILL"} {
			if err := SignalProcessGroup(&os.Process{Pid: pid}, name); err == nil {
				t.Errorf("SignalProcessGroup(%d, %s) = nil, want an error", pid, name)
			}
		}
	}
	if len(*calls) != 0 {
		t.Errorf("seam calls = %v, want none", *calls)
	}
	if err := SignalProcessGroup(&os.Process{Pid: noSuchPid}, "KILL"); err != nil || len(*calls) != 1 {
		t.Errorf("SignalProcessGroup(noSuchPid, KILL) = %v, calls %v, want nil and one call", err, *calls)
	}
}
