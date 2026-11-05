//go:build unix

package main

import (
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/schubydoo/claustrum/internal/supervisor"
	"github.com/schubydoo/claustrum/internal/sysproc"
)

// noSuchPid is the base of the fake pids of the tests. It is above the pid limit of
// Linux and macOS, so it names no process and no group. Tests that need more than
// one fake pid use noSuchPid plus a small number. A fake pid of 1 would turn a group
// signal into kill(-1), which signals every process of the user.
const noSuchPid = 1 << 30

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

// refusedChild is a live record that no exit ends. Its pid is above every pid limit,
// so a signal that misses the seams finds no process.
func refusedChild(m *supervisor.Manager, id string) *supervisor.Proc {
	return m.AddRefusedChildForTest(id, noSuchPid)
}

// A signal that the system refuses answers -32603 with the text of the error, for
// TERM and for KILL (52d82f0a, measured on Linux and macOS with a child that runs as
// root). The kill stamps no killedBy, so a later exit frame has none.
func TestKillAnswersARefusedSignal(t *testing.T) {
	calls := refusingSignals(t, syscall.EPERM)
	s := newTestServer(t)
	p := refusedChild(s.procs, "k1")
	for _, sig := range []string{"TERM", "KILL"} {
		got := dispatchRaw(t, s, `{"jsonrpc":"2.0","id":4,"method":"process.kill","params":{"id":"k1","signal":"`+sig+`"},"auth":"`+testToken+`"}`)
		want := `{"jsonrpc":"2.0","id":4,"error":{"code":-32603,"message":"operation not permitted"}}`
		if got != want {
			t.Errorf("process.kill %s = %s, want %s", sig, got, want)
		}
	}
	if strings.Join(*calls, ",") != "one terminated,group" {
		t.Errorf("signal calls = %v, want the leader TERM and the group KILL", *calls)
	}
	if got := p.KilledByForTest(); got != "" {
		t.Errorf("a refused kill stamped killedBy=%q, want empty", got)
	}
}

// A process that is done, and a pid or group that is gone, keep the success and the
// stamp of before (claustrum's own, not measured).
func TestKillOfAGoneProcessStaysASuccess(t *testing.T) {
	for _, err := range []error{os.ErrProcessDone, syscall.ESRCH} {
		refusingSignals(t, err)
		s := newTestServer(t)
		p := refusedChild(s.procs, "k1")
		got := dispatchRaw(t, s, `{"jsonrpc":"2.0","id":4,"method":"process.kill","params":{"id":"k1","signal":"KILL"},"auth":"`+testToken+`"}`)
		if want := `{"jsonrpc":"2.0","id":4,"result":{"success":true}}`; got != want {
			t.Errorf("process.kill with %v = %s, want %s", err, got, want)
		}
		if got := p.KilledByForTest(); got != "client" {
			t.Errorf("a kill with %v stamped killedBy=%q, want client", err, got)
		}
	}
}
