//go:build linux

package main

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ownChild starts the test binary in a helper mode as a child that leads its own
// group, and waits until it is ready for a SIGTERM. It returns the pid and a channel
// that closes when the child is reaped. The cleanup ends the child by its own group if
// the test left it alive.
func ownChild(t *testing.T, mode, arg string) (pid int, reaped chan struct{}) {
	t.Helper()
	exe, env := helperCommand(t, mode)
	cmd := exec.Command(exe, arg)
	cmd.Env = buildEnv(env)
	cmd.SysProcAttr = newSysProcAttr()
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("helper: %v", err)
	}
	pid = cmd.Process.Pid
	reaped = make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-reaped:
		default:
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			<-reaped
		}
	})
	// "ready" comes after the helper installed its SIGTERM handler.
	line, err := bufio.NewReader(out).ReadString('\n')
	go func() { _ = cmd.Wait(); close(reaped) }()
	if err != nil || line != "ready\n" {
		t.Fatalf("the helper never became ready: %q, %v", line, err)
	}
	return pid, reaped
}

// gatedChild starts a child that, after a SIGTERM, exits with code 0 as soon as the
// test opens the gate. It returns the pid and end, which opens the gate and waits
// until the child is reaped.
func gatedChild(t *testing.T) (pid int, end func()) {
	t.Helper()
	gate := filepath.Join(t.TempDir(), "gate")
	pid, reaped := ownChild(t, "term-exit0-gate", gate)
	return pid, func() {
		if err := os.WriteFile(gate, nil, 0o600); err != nil {
			t.Error(err)
		}
		select {
		case <-reaped:
		case <-time.After(10 * time.Second):
			t.Error("the helper did not end after its SIGTERM")
		}
	}
}

// endChildBeforeRead makes the child end right before the reader opens file of its
// /proc folder: after the stat read, which still showed a live process.
func endChildBeforeRead(t *testing.T, pid int, file string, end func()) {
	t.Helper()
	old := procReadFile
	t.Cleanup(func() { procReadFile = old })
	ended := false
	procReadFile = func(name string) ([]byte, error) {
		if !ended && name == procRoot+"/"+strconv.Itoa(pid)+"/"+file {
			ended = true
			end()
		}
		return old(name)
	}
}

// TestRealReadLiveProcChildEndsBetweenReads pins the read of a process that ends after
// its stat was read: it is gone, not unreadable. A reaped process has no cmdline and
// no environ to open any more. Before, that failed read made the process "not ours".
// The test sends the SIGTERM to the one child it started.
func TestRealReadLiveProcChildEndsBetweenReads(t *testing.T) {
	for _, file := range []string{"cmdline", "environ"} {
		t.Run(file, func(t *testing.T) {
			pid, end := gatedChild(t)
			if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			endChildBeforeRead(t, pid, file, end)
			if lp := realReadLiveProc(pid, true); lp.state != procGone || lp.unreadable != "" {
				t.Errorf("state = %d, unreadable = %q, want procGone (%d) for a child that ended during the read", lp.state, lp.unreadable, procGone)
			}
		})
	}
}

// TestReapChildThatEndsOnSIGTERMIsReaped pins the count of a group that ends on the
// SIGTERM, with a real child and the real reads and signals. The child exits with
// code 0 while the first poll of the grace reads it. It is counted as ended in the
// grace, its record goes, and no line says that it no longer verifies. Measured on
// Linux (rows K5 and ED04a): 89cb6289 logs "1 ended on SIGTERM" there, and the build
// before this fix logged the child as a group that no longer verifies.
//
// The reap signals the group of the one child that this test started, and nothing else.
func TestReapChildThatEndsOnSIGTERMIsReaped(t *testing.T) {
	pid, end := gatedChild(t)
	start := strconv.FormatInt(procStartTicks(pid), 10)
	runDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(runDir, "children"), 0o700); err != nil {
		t.Fatal(err)
	}
	exe, _ := helperCommand(t, "term-exit0-gate")
	tg := reapTarget{pid: pid, start: start, name: strconv.Itoa(pid) + ".json",
		rec: childRecord{Pid: pid, Start: start, Argv0: exe}}
	if err := os.WriteFile(filepath.Join(runDir, "children", tg.name), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	endChildBeforeRead(t, pid, "cmdline", end)

	var counts reapCounts
	out := captureLog(t, func() { reapTargets(simCtx(t, runDir), []reapTarget{tg}, &counts) })

	if counts.signalled != 1 || counts.reapedGrace != 1 || counts.forgotten != 0 || counts.reapedEscalate != 0 || counts.survived != 0 {
		t.Errorf("counts = %+v, want the one group counted as ended in the grace", counts)
	}
	if strings.Contains(out, "no longer verifies") {
		t.Errorf("a child that ended on the SIGTERM was logged as one that no longer verifies:\n%s", out)
	}
	if recordExists(runDir, tg.name) {
		t.Error("the record is still there")
	}
}

// TestReapChildThatReplacesItsProgramGetsNoSIGKILL pins the settle pause before the
// group SIGKILL, with a real Go child that replaces its program on the SIGTERM. The
// stat of such a child can read as state Z for a moment while the child is alive. The
// test forces that reading, through the read seam, on every stat read between the
// SIGTERM and the first pause of the reap. The reap sends no SIGKILL, the child takes
// the normal record test after the pause and gets the no-longer-verifies line, and it
// is alive at the end. Before, two reads in a row with that reading led to a group
// SIGKILL of the live child (Linux row PG05a, 2 of 10 runs).
//
// The reap signals the group of the one child that this test started, and nothing else.
func TestReapChildThatReplacesItsProgramGetsNoSIGKILL(t *testing.T) {
	exe, _ := helperCommand(t, "term-exec")
	other := filepath.Join(t.TempDir(), "ustub")
	if err := os.Symlink(exe, other); err != nil {
		t.Fatal(err)
	}
	pid, reaped := ownChild(t, "term-exec", other)
	start := strconv.FormatInt(procStartTicks(pid), 10)
	runDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(runDir, "children"), 0o700); err != nil {
		t.Fatal(err)
	}
	tg := reapTarget{pid: pid, start: start, name: strconv.Itoa(pid) + ".json",
		rec: childRecord{Pid: pid, Start: start, Argv0: exe}}
	if err := os.WriteFile(filepath.Join(runDir, "children", tg.name), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldKill, oldRead, oldSleep := killGroup, procReadFile, reapSleep
	t.Cleanup(func() { killGroup, procReadFile, reapSleep = oldKill, oldRead, oldSleep })
	var kills []killRec
	termed, paused := false, false
	killGroup = func(p int, sig syscall.Signal) error {
		kills = append(kills, killRec{p, sig})
		err := oldKill(p, sig)
		termed = termed || sig == syscall.SIGTERM
		return err
	}
	dir := procRoot + "/" + strconv.Itoa(pid)
	procReadFile = func(name string) ([]byte, error) {
		b, err := oldRead(name)
		if i := strings.LastIndexByte(string(b), ')'); err == nil && termed && !paused && name == dir+"/stat" && i >= 0 && len(b) > i+2 {
			b[i+2] = 'Z' // the state letter, as a read in the middle of the replacement shows it
		}
		return b, err
	}
	reapSleep = func(d time.Duration) {
		oldSleep(d)
		if !termed || paused {
			return
		}
		// The first pause after the SIGTERM. Wait here until the replacement is over.
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if b, err := os.ReadFile(dir + "/cmdline"); err == nil && strings.HasPrefix(string(b), other+"\x00") {
				break
			}
		}
		paused = true
	}

	var counts reapCounts
	out := captureLog(t, func() { reapTargets(simCtx(t, runDir), []reapTarget{tg}, &counts) })

	if len(kills) != 1 || kills[0] != (killRec{pid, syscall.SIGTERM}) {
		t.Errorf("signals = %v, want one SIGTERM to the group of the child and nothing more", kills)
	}
	if want := "[process.Registry] group " + strconv.Itoa(pid) + " no longer verifies (it does not run the recorded program \"" + exe + "\"); nothing more is sent to it\n"; !strings.Contains(out, want) {
		t.Errorf("log = %q\nwant it to hold %q", out, want)
	}
	if counts.forgotten != 1 || counts.reapedGrace != 0 || counts.reapedEscalate != 0 {
		t.Errorf("counts = %+v, want the group counted in forgotten only", counts)
	}
	select {
	case <-reaped:
		t.Error("the child ended. It must be alive after a reap that sent it only the SIGTERM")
	default:
		if b, err := os.ReadFile(dir + "/cmdline"); err != nil || !strings.HasPrefix(string(b), other+"\x00") {
			t.Errorf("cmdline = %q, %v, want the child alive as its new program %s", b, err, other)
		}
	}
}
