//go:build linux

package hostsweep

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/procinfo"
)

// The orphans with a CLAUDE_SSH_CHILD stamp and the wait before the group SIGKILL of a gone
// leader (c9125eb8, measured on Linux). Every signal goes through a recording seam, to fake
// pids above the pid limit.

// hcStampedOrphan stages an old orphan group leader with stream-json argv, the daemon-child
// marker, pipe stdio, the environment entry stamp ("" for none) and the entries extra. Its
// start-ticks are "1".
func hcStampedOrphan(t *testing.T, f *hcPassFix, pid int, exe, stamp string, extra ...string) {
	t.Helper()
	hcFakeDaemonUID(t, f.proot, f.mk, f.link, pid, exe, []string{"claude", "--output-format", "stream-json", "--verbose"}, true, "1", f.uid)
	env := "PATH=/bin\x00CLAUDE_SSH_DAEMON_CHILD=1\x00"
	for _, e := range extra {
		env += e + "\x00"
	}
	if stamp != "" {
		env += stamp + "\x00"
	}
	f.mk(pid, "environ", env)
	hcPipeStdio(f.link, pid)
}

// TestPassEndsAStampedOrphanOfAnotherProgram: an orphan whose program is no CLI is ended when
// it carries its own stamp, with the measured line. An orphan of that program with no stamp
// is spared (c9125eb8, measured on Linux). A stamp of another pid or another start is
// spared too (claustrum's own, not measured). A stamped CLI orphan keeps the CLI line.
func TestPassEndsAStampedOrphanOfAnotherProgram(t *testing.T) {
	f := hcNewPassFixture(t)
	launcher := filepath.Join(f.base, "launcher", "stubl")
	cli := filepath.Join(f.base, "ccd-cli", "2.0.1")
	marked, bare, otherPid, otherStart, cliPid := noSuchPid+101, noSuchPid+102, noSuchPid+103, noSuchPid+104, noSuchPid+105
	stamp := func(pid int, start string) string { return "CLAUDE_SSH_CHILD=" + strconv.Itoa(pid) + ":" + start }
	own := "CLAUDE_SSH_RUN_DIR=" + f.c.ownRunDir
	hcStampedOrphan(t, f, marked, launcher, stamp(marked, "1"), own)
	hcStampedOrphan(t, f, bare, launcher, "", own)
	hcStampedOrphan(t, f, otherPid, launcher, stamp(otherPid+1, "1"), own)
	hcStampedOrphan(t, f, otherStart, launcher, stamp(otherStart, "2"), own)
	hcStampedOrphan(t, f, cliPid, cli, stamp(cliPid, "1"))
	buf := captureLogBuf(t)

	sum := f.c.Pass()

	slices.Sort(f.group)
	if want := []int{marked, cliPid}; !slices.Equal(f.group, want) {
		t.Fatalf("group signals = %v, want %v", f.group, want)
	}
	if sum.orphanSignalled != 2 || sum.orphanSurvived != 0 {
		t.Errorf("summary signalled %d survived %d, want 2 and 0", sum.orphanSignalled, sum.orphanSurvived)
	}
	got := buf.String()
	for _, want := range []string{
		fmt.Sprintf("[hostclean] ending orphaned Claude Code process group %d (its daemon is gone; known by its CLAUDE_SSH_CHILD mark, not by its program): SIGTERM\n", marked),
		fmt.Sprintf("[hostclean] ending orphaned Claude Code process group %d (its daemon is gone): SIGTERM\n", cliPid),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log lacks %q\nlog: %s", want, got)
		}
	}
	for _, pid := range []int{bare, otherPid, otherStart} {
		if strings.Contains(got, strconv.Itoa(pid)) {
			t.Errorf("log names the spared group %d\nlog: %s", pid, got)
		}
	}
}

// TestPassEndsAStampedOrphanOfAKnownRunDir: a stamped orphan of a program that is no CLI
// binary is ended when any CLAUDE_SSH_RUN_DIR entry names a run dir under the root. The own
// run dir first, last or in the middle counts, and so does the run dir of a gone daemon. No
// entry, another folder, the run root and `<root>/run/../x` are spared with no line
// (c9125eb8, measured on Linux and macOS). A deeper path, `<root>/run/.` and `<root>/run/..`
// are spared (claustrum's own, not measured). A CLI orphan with no entry still ends (c9125eb8, measured
// on Linux and macOS).
func TestPassEndsAStampedOrphanOfAKnownRunDir(t *testing.T) {
	f := hcNewPassFixture(t)
	launcher := filepath.Join(f.base, "launcher", "stubl")
	cli := filepath.Join(f.base, "ccd-cli", "2.0.1")
	own := f.c.ownRunDir
	gone := filepath.Join(f.runRoot, "c9")
	rows := []struct {
		pid   int
		exe   string
		env   []string
		ended bool
	}{
		{noSuchPid + 401, launcher, []string{"CLAUDE_SSH_RUN_DIR=" + own}, true},
		{noSuchPid + 402, launcher, nil, false},
		{noSuchPid + 403, launcher, []string{"CLAUDE_SSH_RUN_DIR=/tmp/x"}, false},
		{noSuchPid + 404, launcher, []string{"CLAUDE_SSH_RUN_DIR=" + f.runRoot}, false},
		{noSuchPid + 405, launcher, []string{"CLAUDE_SSH_RUN_DIR=" + f.runRoot + "/../x"}, false},
		{noSuchPid + 406, launcher, []string{"CLAUDE_SSH_RUN_DIR=/tmp/outside-x", "CLAUDE_SSH_RUN_DIR=" + own}, true},
		{noSuchPid + 407, launcher, []string{"CLAUDE_SSH_RUN_DIR=" + own, "CLAUDE_SSH_RUN_DIR=/tmp/outside-x"}, true},
		{noSuchPid + 408, cli, nil, true},
		{noSuchPid + 409, launcher, []string{"CLAUDE_SSH_RUN_DIR=/tmp/outside-x", "CLAUDE_SSH_RUN_DIR=" + own, "CLAUDE_SSH_RUN_DIR=/tmp/outside-y"}, true},
		{noSuchPid + 410, launcher, []string{"CLAUDE_SSH_RUN_DIR=" + gone}, true},
		{noSuchPid + 411, launcher, []string{"CLAUDE_SSH_RUN_DIR=" + gone + "/x"}, false},
		{noSuchPid + 412, launcher, []string{"CLAUDE_SSH_RUN_DIR=" + f.runRoot + "/.."}, false},
		{noSuchPid + 413, launcher, []string{"CLAUDE_SSH_RUN_DIR=" + f.runRoot + "/."}, false},
	}
	for _, r := range rows {
		hcStampedOrphan(t, f, r.pid, r.exe, "CLAUDE_SSH_CHILD="+strconv.Itoa(r.pid)+":1", r.env...)
	}
	buf := captureLogBuf(t)

	sum := f.c.Pass()

	var want []int
	for _, r := range rows {
		if r.ended {
			want = append(want, r.pid)
		}
	}
	slices.Sort(f.group)
	if !slices.Equal(f.group, want) {
		t.Fatalf("group signals = %v, want %v", f.group, want)
	}
	if sum.orphanSignalled != len(want) || sum.undecided != 0 {
		t.Errorf("summary signalled %d undecided %d, want %d and 0", sum.orphanSignalled, sum.undecided, len(want))
	}
	got := buf.String()
	for _, r := range rows {
		if !r.ended && strings.Contains(got, strconv.Itoa(r.pid)) {
			t.Errorf("log names the spared group %d\nlog: %s", r.pid, got)
		}
	}
	for _, pid := range []int{noSuchPid + 401, noSuchPid + 406, noSuchPid + 407, noSuchPid + 409, noSuchPid + 410} {
		if line := fmt.Sprintf(hcOrphanMarkLog, pid) + "\n"; !strings.Contains(got, line) {
			t.Errorf("log lacks %q\nlog: %s", line, got)
		}
	}
}

// hcGoneLeaderFix records the group probes and the SIGKILL times of one group leader.
type hcGoneLeaderFix struct {
	pid    int
	start  time.Time
	now    time.Time
	probes int
	kills  []time.Duration
}

// hcNewGoneLeaderFix seams the clock, the sleep and the group signals for one group leader
// that exits on its SIGTERM. memberUntil says how long a member of its group stays after the
// SIGTERM. A negative value keeps the member until a SIGKILL.
func hcNewGoneLeaderFix(t *testing.T, memberUntil time.Duration) *hcGoneLeaderFix {
	t.Helper()
	root, mk, _ := fakeProc(t)
	f := &hcGoneLeaderFix{pid: noSuchPid + 201}
	oldGrp, oldClock, oldSleep := procinfo.KillGroupForTest(), hcClock, hcSleep
	t.Cleanup(func() { procinfo.SetKillGroupForTest(oldGrp); hcClock, hcSleep = oldClock, oldSleep })
	f.start = time.Unix(4_000_000, 0)
	f.now = f.start
	hcClock = func() time.Time { return f.now }
	hcSleep = func(d time.Duration) { f.now = f.now.Add(d) }
	mk(f.pid, "stat", hcRunningStat(f.pid, 1, f.pid, "1"))
	procinfo.SetKillGroupForTest(func(pid int, sig syscall.Signal) error {
		switch sig {
		case syscall.SIGTERM:
			_ = os.RemoveAll(filepath.Join(root, strconv.Itoa(pid))) // the leader exits at once
		case syscall.SIGKILL:
			f.kills = append(f.kills, f.now.Sub(f.start))
		case 0:
			f.probes++
			if len(f.kills) > 0 || (memberUntil >= 0 && f.now.Sub(f.start) >= memberUntil) {
				return syscall.ESRCH
			}
		}
		return nil
	})
	return f
}

func (f *hcGoneLeaderFix) run() (signalled, survived int) {
	return endGroupsTwoPhase([]hcGroup{{tracked: tracked{pid: f.pid, pgid: f.pid, startTicks: "1"}}}, "orphaned process group")
}

// TestGoneLeaderGroupThatEndsGetsNoSIGKILL: the group of a gone leader that is gone at the
// first probe gets no SIGKILL (c9125eb8, measured on Linux).
func TestGoneLeaderGroupThatEndsGetsNoSIGKILL(t *testing.T) {
	f := hcNewGoneLeaderFix(t, 0)
	if sig, surv := f.run(); sig != 1 || surv != 0 {
		t.Errorf("signalled %d survived %d, want 1 and 0", sig, surv)
	}
	if len(f.kills) != 0 {
		t.Errorf("SIGKILL at %v, want none", f.kills)
	}
	if f.probes != 1 {
		t.Errorf("group probes = %d, want 1", f.probes)
	}
}

// TestGoneLeaderGroupThatEndsInTheWaitGetsNoSIGKILL: a member that ends 1 s after the
// SIGTERM ends the wait with no SIGKILL. The group is probed every 100 ms until then.
func TestGoneLeaderGroupThatEndsInTheWaitGetsNoSIGKILL(t *testing.T) {
	f := hcNewGoneLeaderFix(t, time.Second)
	f.run()
	if len(f.kills) != 0 {
		t.Errorf("SIGKILL at %v, want none", f.kills)
	}
	if f.probes != 11 {
		t.Errorf("group probes = %d, want 11 (0 ms to 1 s, every 100 ms)", f.probes)
	}
}

// TestGoneLeaderGroupWithAMemberGetsSIGKILLAfterTheWait: a member that ignores SIGTERM gets
// one group SIGKILL 3 s after the SIGTERM and no line (c9125eb8, measured on Linux).
func TestGoneLeaderGroupWithAMemberGetsSIGKILLAfterTheWait(t *testing.T) {
	f := hcNewGoneLeaderFix(t, -1)
	buf := captureLogBuf(t)
	if sig, surv := f.run(); sig != 1 || surv != 0 {
		t.Errorf("signalled %d survived %d, want 1 and 0", sig, surv)
	}
	if len(f.kills) != 1 || f.kills[0] != hcTermGrace {
		t.Fatalf("SIGKILL at %v, want one at %s", f.kills, hcTermGrace)
	}
	if f.probes < 30 {
		t.Errorf("group probes = %d, want at least 30 before the SIGKILL", f.probes)
	}
	if got := buf.String(); strings.Contains(got, "SIGKILL") {
		t.Errorf("log = %q, want no SIGKILL line", got)
	}
}

// TestGoneLeaderReusedPidGetsNoSIGKILL: the identity test before the group SIGKILL. A leader
// pid that belongs to another process at the end of the wait gets no SIGKILL.
func TestGoneLeaderReusedPidGetsNoSIGKILL(t *testing.T) {
	root, mk, _ := fakeProc(t)
	f := &hcGoneLeaderFix{pid: noSuchPid + 202}
	oldGrp, oldClock, oldSleep := procinfo.KillGroupForTest(), hcClock, hcSleep
	t.Cleanup(func() { procinfo.SetKillGroupForTest(oldGrp); hcClock, hcSleep = oldClock, oldSleep })
	f.start = time.Unix(4_000_000, 0)
	f.now = f.start
	hcClock = func() time.Time { return f.now }
	hcSleep = func(d time.Duration) {
		f.now = f.now.Add(d)
		if f.now.Sub(f.start) >= 2*time.Second { // the pid now names another process
			mk(f.pid, "stat", hcRunningStat(f.pid, 1, f.pid, "9999"))
		}
	}
	mk(f.pid, "stat", hcRunningStat(f.pid, 1, f.pid, "1"))
	procinfo.SetKillGroupForTest(func(pid int, sig syscall.Signal) error {
		switch sig {
		case syscall.SIGTERM:
			_ = os.RemoveAll(filepath.Join(root, strconv.Itoa(pid)))
		case syscall.SIGKILL:
			f.kills = append(f.kills, f.now.Sub(f.start))
		}
		return nil // the group still answers
	})
	f.run()
	if len(f.kills) != 0 {
		t.Errorf("SIGKILL at %v to a reused pid, want none", f.kills)
	}
}

// TestPassSkipsAStoppedStampedNonStreamGroupSilently: a stopped group of a program that is no
// CLI binary, with its own stamp and no stream-json argv, gets no line and no undecided count
// (claustrum's own, not measured). The control, the same group with stream-json, gets the
// stopped line, so the instrument sees a line when one comes.
func TestPassSkipsAStoppedStampedNonStreamGroupSilently(t *testing.T) {
	f := hcNewPassFixture(t)
	launcher := filepath.Join(f.base, "launcher", "stubl")
	plain, stream := noSuchPid+301, noSuchPid+302
	for _, pid := range []int{plain, stream} {
		hcStampedOrphan(t, f, pid, launcher, "CLAUDE_SSH_CHILD="+strconv.Itoa(pid)+":1", "CLAUDE_SSH_RUN_DIR="+f.c.ownRunDir)
		f.mk(pid, "stat", fmt.Sprintf("%d (sh) T 1 %d %s1 x", pid, pid, strings.Repeat("0 ", 16)))
	}
	f.mk(plain, "cmdline", "sh\x00-c\x00sleep 300\x00")
	buf := captureLogBuf(t)

	sum := f.c.Pass()

	got := buf.String()
	if strings.Contains(got, strconv.Itoa(plain)) {
		t.Errorf("log names the plain group %d\nlog: %s", plain, got)
	}
	if want := fmt.Sprintf("[hostclean] spared process group %d this sweep: could not read it (it is stopped)\n", stream); !strings.Contains(got, want) {
		t.Errorf("log lacks the control line %q\nlog: %s", want, got)
	}
	if sum.undecided != 1 {
		t.Errorf("undecided = %d, want 1 (the control only)", sum.undecided)
	}
	if len(f.group) != 0 {
		t.Errorf("group signals = %v, want none", f.group)
	}
}
