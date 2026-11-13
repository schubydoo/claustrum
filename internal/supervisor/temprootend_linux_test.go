package supervisor

import (
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

// TestLostTempRootKillsTheGroupOfAChildThatEnds: a child that ends on the SIGTERM of
// the end gets a group SIGKILL before its reap, and a process in its group ends with
// it. The exit frame carries the killedBy "temp_root" (claustrum's rule,
// decision-099). The grandchild is the test's own.
func TestLostTempRootKillsTheGroupOfAChildThatEnds(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	setTempRootEndGraceForTest(t, 10*time.Second)
	sent := recordSignalsByProc(t)
	pidFile := filepath.Join(t.TempDir(), "kid")
	c, lines := rawLineConn(t)
	p, bound := spawnHelperTempRoot(t, m, c, TempRootAsk{Folder: true, End: true}, "term-exit0-kid", pidFile)
	nextLine(t, lines, `"stream":"stdout"`, 20*time.Second)
	kid := waitPIDFile(t, pidFile)
	gone := false
	t.Cleanup(func() {
		if !gone {
			_ = syscall.Kill(kid, syscall.SIGKILL)
		}
	})
	end, _ := bound()
	end()
	exit := withoutEndAt(nextLine(t, lines, `"stream":"exit"`, 15*time.Second))
	const want = `{"type":"stream","processId":"tr","stream":"exit","seq":2,"exitCode":0,"killedBy":"temp_root","end":{"at":<n>,"len":93}}`
	if exit != want {
		t.Errorf("exit frame = %s\nwant %s", exit, want)
	}
	if got := sent(p.cmd.Process); !slices.Equal(got, []string{"TERM", "KILL"}) {
		t.Errorf("signals = %q, want TERM, then the group KILL", got)
	}
	for deadline := time.Now().Add(5 * time.Second); !gone; time.Sleep(20 * time.Millisecond) {
		if syscall.Kill(kid, 0) != nil {
			gone = true
		} else if time.Now().After(deadline) {
			t.Fatalf("the process %d in the group lives on", kid)
		}
	}
}

// TestNoTempRootGroupKillAfterAClientEnd: a cliTempRootEnd child that a process.kill
// ends, with no loss of its folder, gets no group SIGKILL at its exit (claustrum's own).
func TestNoTempRootGroupKillAfterAClientEnd(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	sent := recordSignalsByProc(t)
	c, lines := rawLineConn(t)
	p, _ := spawnHelperTempRoot(t, m, c, TempRootAsk{Folder: true, End: true}, "term-exit0")
	nextLine(t, lines, `"stream":"stdout"`, 20*time.Second)
	p.signalIfLive("TERM", "client")
	nextLine(t, lines, `"stream":"exit"`, 15*time.Second)
	if got := sent(p.cmd.Process); !slices.Equal(got, []string{"TERM"}) {
		t.Errorf("signals = %q, want the TERM alone", got)
	}
}
