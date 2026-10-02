//go:build linux || darwin

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestRecordMatchesLive pins which live process a record with a program key accepts.
// Each case is a Linux row of the 89cb6289 measurement. <F> is /f here.
func TestRecordMatchesLive(t *testing.T) {
	const stub, target = "/f/bin/sigstub", "/f/bin/target"
	stubArgs := []string{"--log", "/r/c1.log"}
	for _, tc := range []struct {
		row            string
		argv0, program string
		live           string
		args           []string
		want           bool
	}{
		{"PG02a launcher replaced itself with the command", "/f/bin/wrap", stub, stub, stubArgs, true},
		{"PG02b launcher shell still runs and holds the command", "/f/bin/wrapstay", stub, "/bin/sh", []string{"/f/bin/wrapstay", stub, "--log", "/r/c1.log"}, true},
		{"ED03a program is the full path of the process", "/nonexistent/other", stub, stub, stubArgs, true},
		{"ED03b program matches by base name", "/nonexistent/other", "/elsewhere/sigstub", stub, stubArgs, true},
		{"ED04a program is one whole argument", "/nonexistent/other", target, stub, append(stubArgs[:2:2], target), true},
		{"ED04b a cleaned path of an argument does not match", "/nonexistent/other", "/f/bin/../bin/target", stub, append(stubArgs[:2:2], target), false},
		{"ED04c a base name of an argument does not match", "/nonexistent/other", "target", stub, append(stubArgs[:2:2], target), false},
		{"ED04d a part of an argument does not match", "/nonexistent/other", target, stub, append(stubArgs[:2:2], "--cli="+target), false},
		{"ED04e an argument with a space matches whole", "/nonexistent/other", "/f/bin/my target", stub, append(stubArgs[:2:2], "/f/bin/my target"), true},
		{"ED06a empty program, argv0 matches", stub, "", stub, stubArgs, true},
		{"ED06b empty program, argv0 does not match", "/nonexistent/other", "", stub, stubArgs, false},
		{"PG01b no program key, a shell runs the recorded script", "/f/bin/shstay", "", "/bin/sh", []string{"/f/bin/shstay", "--log", "/r/c1.log"}, false},
		{"ED01a no program key, another program", "/nonexistent/other", "", stub, stubArgs, false},
	} {
		t.Run(tc.row, func(t *testing.T) {
			rec := childRecord{Argv0: tc.argv0, Program: tc.program}
			lp := liveProc{state: procAlive, program: tc.live, args: tc.args}
			if got := recordMatchesLive(rec, lp); got != tc.want {
				t.Errorf("recordMatchesLive(argv0 %q, program %q; live %q %q) = %v, want %v",
					tc.argv0, tc.program, tc.live, tc.args, got, tc.want)
			}
		})
	}
}

// psLive is a live process as the darwin reader builds it from one command text.
func psLive(text string) liveProc {
	lp := liveProc{state: procAlive, cmdText: text}
	lp.program, lp.args = splitCommandText(text)
	return lp
}

// TestRecordMatchesCommandText pins the record test of darwin, which judges a process
// by the command text that `ps` prints and by its words, cut at blanks. The rows are
// macOS rows of the 89cb6289 measurement. A case with no row follows from the rule
// and says so. The test runs on linux and darwin: it covers the rule on a given text.
// It does not cover what a real `ps` prints for a blank or a tab.
func TestRecordMatchesCommandText(t *testing.T) {
	const stub = "/tmp/e/bin/sigstub --log /tmp/e/c1.log"
	for _, tc := range []struct {
		name           string
		argv0, program string
		text           string
		want           bool
	}{
		// p equals the whole text.
		{"whole text, a blank in the path, no arguments", "/tmp/e/bin/my stub", "", "/tmp/e/bin/my stub", true},
		{"whole text without a blank", "/tmp/e/bin/sigstub", "", "/tmp/e/bin/sigstub", true},
		// The text starts with p and one blank.
		{"PG04s the text starts with the program and a blank", "/tmp/e/bin/my stub", "", "/tmp/e/bin/my stub --log x", true},
		{"PG02s the same through the program key", "/tmp/e/bin/wrap", "/tmp/e/bin/my stub", "/tmp/e/bin/my stub --log x", true},
		{"a prefix with no blank after it", "/tmp/e/bin/m", "", "/tmp/e/bin/my stub --log x", false},
		{"a prefix that ends inside the second word", "/tmp/e/bin/my st", "", "/tmp/e/bin/my stub --log x", false},
		{"follows from the rule (cell B2): the first word alone", "/tmp/e/bin/my", "", "/tmp/e/bin/my stub --log x", true},
		// Two blanks in a row (macOS rows B4a and B4b), and a raw tab. A real `ps`
		// prints a tab as the four characters \011 (macOS row B4c), so the two tab
		// cases pin only how claustrum cuts a text that holds a raw tab.
		{"two blanks in the path, record with two", "/tmp/e/bin/my  stub", "", "/tmp/e/bin/my  stub --log x", true},
		{"two blanks in the path, record with one", "/tmp/e/bin/my stub", "", "/tmp/e/bin/my  stub --log x", false},
		{"a tab in the path, record with the tab", "/tmp/e/bin/my\tstub", "", "/tmp/e/bin/my\tstub --log x", true},
		{"a tab is not a blank: the part before it is not the first word", "/tmp/e/bin/my", "", "/tmp/e/bin/my\tstub --log x", false},
		// A handed word.
		{"ED04a a handed word with no blank", "/nonexistent/other", "/tmp/e/bin/target", stub + " /tmp/e/bin/target", true},
		{"ED04e a program with a blank is never handed", "/nonexistent/other", "/tmp/e/bin/my target", stub + " /tmp/e/bin/my target", false},
		{"PG02b a launcher shell holds the program as a word", "/tmp/e/bin/wrapstay", "/tmp/e/bin/sigstub", "/bin/sh /tmp/e/bin/wrapstay " + stub, true},
		{"ED04d a part of a word is not handed", "/nonexistent/other", "/tmp/e/bin/target", stub + " --cli=/tmp/e/bin/target", false},
		// The first word against a later word.
		{"argv0 equals a later word only", "/tmp/e/bin/shstay", "", "/bin/sh /tmp/e/bin/shstay --log x", false},
		{"ED03a the program is the first word", "/nonexistent/other", "/tmp/e/bin/sigstub", stub, true},
		{"ED01a another program", "/nonexistent/other", "", stub, false},
		// Equal base names in other folders.
		{"PG05b the same base name in another folder", "/tmp/e/bin/sigstub", "", "/tmp/e/alt/sigstub --log x", true},
		{"a blank in the folder of the record", "/tmp/my dir/sigstub", "", "/tmp/e/alt/sigstub --log x", true},
		{"a blank in the folder of the live program: its first word ends there", "/tmp/other/sigstub", "", "/tmp/my dir/sigstub --log x", false},
		{"a blank in the folder of the live program, same path in the record", "/tmp/my dir/sigstub", "", "/tmp/my dir/sigstub --log x", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := childRecord{Argv0: tc.argv0, Program: tc.program}
			if got := recordMatchesLive(rec, psLive(tc.text)); got != tc.want {
				t.Errorf("recordMatchesLive(argv0 %q, program %q; text %q) = %v, want %v", tc.argv0, tc.program, tc.text, got, tc.want)
			}
		})
	}
	// The control: with no command text, as on linux, a program with a blank is one
	// whole argument and is handed (Linux row ED04e), and a prefix of argv[0] is no match.
	linux := liveProc{state: procAlive, program: "/tmp/e/bin/sigstub", args: []string{"--log", "x", "/tmp/e/bin/my target"}}
	if !recordMatchesLive(childRecord{Argv0: "/nonexistent/other", Program: "/tmp/e/bin/my target"}, linux) {
		t.Error("linux: a whole argument with a blank was not handed")
	}
	if recordMatchesLive(childRecord{Argv0: "/tmp/e/bin/my"}, liveProc{state: procAlive, program: "/tmp/e/bin/my stub"}) {
		t.Error("linux: a prefix of argv[0] matched")
	}
}

// TestParsePSLine pins how one `ps -o pgid=,stat=,command=` line is cut: the padding of
// the first two columns goes, and the command text keeps its own blanks.
func TestParsePSLine(t *testing.T) {
	for _, tc := range []struct {
		line       string
		pgid       int
		stat, text string
		ok         bool
	}{
		{"54321 S /usr/bin/node", 54321, "S", "/usr/bin/node", true},
		{"  812 Ss+  /tmp/e/bin/my  stub --log x", 812, "Ss+", "/tmp/e/bin/my  stub --log x", true},
		{"54321 S    /tmp/e/bin/my\tstub", 54321, "S", "/tmp/e/bin/my\tstub", true},
		{"54321 S", 0, "", "", false},
		{"54321 S   ", 0, "", "", false},
		{"54321", 0, "", "", false},
		{"", 0, "", "", false},
	} {
		pgid, stat, text, ok := parsePSLine(tc.line)
		if ok != tc.ok || (ok && (pgid != tc.pgid || stat != tc.stat || text != tc.text)) {
			t.Errorf("parsePSLine(%q) = %d, %q, %q, %v, want %d, %q, %q, %v", tc.line, pgid, stat, text, ok, tc.pgid, tc.stat, tc.text, tc.ok)
		}
	}
}

// TestVerifyOrphanEmptyArgv0BeforeProgram pins row ED02c: a record with an empty argv0
// is refused before its program key is read, also when the live process runs program.
func TestVerifyOrphanEmptyArgv0BeforeProgram(t *testing.T) {
	const pid, runDir = 424242, "/run/c1"
	fakeLiveProcs(t, map[int]liveProc{pid: {
		state: procAlive, startTicks: "9", pgid: pid, program: "/f/bin/sigstub",
		runDir: runDir, childMark: strconv.Itoa(pid) + ":9",
	}})
	rec := childRecord{Pid: pid, Start: "9", Argv0: "", Program: "/f/bin/sigstub"}
	v, reason := verifyOrphan(rec, runDir, simOwnDaemon, simOwnParent, simOwnPgid)
	if v != verdictSkip || reason != "record names no program to verify against" {
		t.Errorf("verdict/reason = %d/%q, want a skip for no program", v, reason)
	}
}

// orphanRecord is a record of a dead predecessor daemon on this machine and boot, for
// a sweep test. The pids are far above any real pid, and every signal is seamed.
func orphanRecord(pid int, argv0, program string) childRecord {
	node, host := ownReapIdentity()
	return childRecord{
		Pid: pid, Node: node, Host: host, Instance: "inst-old",
		DaemonPid: 999999999, DaemonStart: "1", Argv0: argv0, Program: program,
		Start: "9000", At: time.Now().UnixMilli(),
	}
}

// TestReapOrphansEndsLauncherChild is the sweep for the gap that this rule closes: the
// daemon writes a program key for a child that a launcher started, and the next start
// must accept that record. Before, the sweep read argv0 only, so every launcher child
// outlived a restart (89cb6289, Linux rows PG02a, PG02b and RP15g).
func TestReapOrphansEndsLauncherChild(t *testing.T) {
	if node, _ := ownReapIdentity(); node == "" {
		t.Skip("no node identity on this host")
	}
	runDir := t.TempDir()
	const replaced, stays = 800000001, 800000002
	writeRec(t, runDir, orphanRecord(replaced, "/f/bin/wrap", "/f/bin/sigstub"))
	writeRec(t, runDir, orphanRecord(stays, "/f/bin/wrapstay", "/f/bin/sigstub"))
	sim := newProcSim(t)
	// The launcher replaced itself with the command.
	sim.live[replaced] = &liveProc{state: procAlive, startTicks: "9000", pgid: replaced,
		program: "/f/bin/sigstub", args: []string{"--log", "x"},
		runDir: runDir, childMark: strconv.Itoa(replaced) + ":9000"}
	// The launcher is a shell script that still runs and waits for the command.
	sim.live[stays] = &liveProc{state: procAlive, startTicks: "9000", pgid: stays,
		program: "/bin/sh", args: []string{"/f/bin/wrapstay", "/f/bin/sigstub", "--log", "x"},
		runDir: runDir, childMark: strconv.Itoa(stays) + ":9000"}
	sim.diesOn(replaced, syscall.SIGTERM)
	sim.diesOn(stays, syscall.SIGTERM)

	reapOrphans(runDir, "inst-self")

	for _, pid := range []int{replaced, stays} {
		if !sim.signaled(pid) {
			t.Errorf("launcher child %d got no signal; kills = %v", pid, sim.kills)
		}
	}
	if sim.sigCount(syscall.SIGTERM) != 2 || sim.sigCount(syscall.SIGKILL) != 0 {
		t.Errorf("kills = %v, want one SIGTERM for each of the two groups", sim.kills)
	}
}

// TestReapOrphansSymlinkedRecord pins that the sweep reads no record through a symlink.
// The symlink has a <pid>.json name and leads to a valid record outside children/. The
// live process passes every test of the sweep, so only the kind of the file can stop
// the signal. The sweep removes the symlink, keeps its target and sends nothing
// (89cb6289, Linux row ED16b). Before, the sweep read through the symlink and sent
// SIGTERM to the group that the outside record names.
func TestReapOrphansSymlinkedRecord(t *testing.T) {
	if node, _ := ownReapIdentity(); node == "" {
		t.Skip("no node identity on this host")
	}
	runDir := t.TempDir()
	children := filepath.Join(runDir, "children")
	if err := os.Mkdir(children, 0o700); err != nil {
		t.Fatal(err)
	}
	const linked = 800000011
	outside := t.TempDir()
	writeRec(t, outside, orphanRecord(linked, "/f/bin/sigstub", ""))
	target := filepath.Join(outside, "children", strconv.Itoa(linked)+".json")
	if err := os.Symlink(target, filepath.Join(children, strconv.Itoa(linked)+".json")); err != nil {
		t.Fatal(err)
	}
	sim := newProcSim(t)
	sim.live[linked] = &liveProc{state: procAlive, startTicks: "9000", pgid: linked,
		program: "/f/bin/sigstub", runDir: runDir, childMark: strconv.Itoa(linked) + ":9000"}
	sim.diesOn(linked, syscall.SIGTERM)

	reapOrphans(runDir, "inst-self")

	if len(sim.kills) != 0 {
		t.Errorf("the sweep sent signals for a record that is a symlink: %v", sim.kills)
	}
	if names := childrenNames(t, children); len(names) != 0 {
		t.Errorf("entries left in children/: %v, want the symlink removed", names)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("the target of the symlink is gone: %v", err)
	}
}

// TestReapOrphansFIFOAndFolder pins a FIFO and a folder with a <pid>.json name: the
// sweep removes each one, sends nothing, and does not wait on the FIFO (89cb6289,
// Linux rows ED16a and ED16c). Before, the read of the FIFO waited for a writer.
func TestReapOrphansFIFOAndFolder(t *testing.T) {
	runDir := t.TempDir()
	children := filepath.Join(runDir, "children")
	if err := os.Mkdir(children, 0o700); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(children, "800000012.json")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(children, "800000013.json"), 0o775); err != nil {
		t.Fatal(err)
	}
	sim := newProcSim(t)

	done := make(chan struct{})
	go func() { defer close(done); reapOrphans(runDir, "inst-self") }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		// A sweep that opened the FIFO to read it waits for a writer. Give it one, so
		// the goroutine ends, then fail.
		if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = w.Close()
		}
		<-done
		t.Fatal("the sweep waited on the FIFO")
	}

	if len(sim.kills) != 0 {
		t.Errorf("the sweep sent signals: %v", sim.kills)
	}
	if names := childrenNames(t, children); len(names) != 0 {
		t.Errorf("entries left in children/: %v, want the FIFO and the folder removed", names)
	}
}

// TestReadChildRecordRefusesNonRegular pins the second guard, inside the read: the
// open follows no symlink and does not wait on a FIFO, so an entry that changes kind
// after the directory read is still not read as a record.
func TestReadChildRecordRefusesNonRegular(t *testing.T) {
	runDir := t.TempDir()
	children := filepath.Join(runDir, "children")
	if err := os.Mkdir(children, 0o700); err != nil {
		t.Fatal(err)
	}
	outsideRec := filepath.Join(runDir, "real.json")
	if err := os.WriteFile(outsideRec, []byte(`{"pid":4242,"start":"1","argv0":"a"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideRec, filepath.Join(children, "4242.json")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(children, "4243.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The FIFO has a writer that puts a valid record into it and then closes. A read
	// that opens the FIFO gets that record. The read under test must not open it.
	writer, err := os.OpenFile(filepath.Join(children, "4243.json"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteString(`{"pid":4243,"start":"1","argv0":"a"}`); err != nil {
		t.Fatal(err)
	}
	closeWriter := time.AfterFunc(100*time.Millisecond, func() { _ = writer.Close() })
	t.Cleanup(func() { closeWriter.Stop(); _ = writer.Close() })
	// A symlink that stays inside the children folder is not a record either.
	if err := os.WriteFile(filepath.Join(children, "target.txt"), []byte(`{"pid":4245,"start":"1","argv0":"a"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", filepath.Join(children, "4245.json")); err != nil {
		t.Fatal(err)
	}
	c := simCtx(t, runDir)
	type result struct{ link, fifo bool }
	done := make(chan result, 1)
	go func() {
		_, link := c.read("4242.json")
		_, fifo := c.read("4243.json")
		_, inside := c.read("4245.json")
		if inside {
			link = true
		}
		done <- result{link, fifo}
	}()
	select {
	case r := <-done:
		if r.link {
			t.Error("the record read went through a symlink")
		}
		if r.fifo {
			t.Error("the record read took a FIFO for a record")
		}
	case <-time.After(10 * time.Second):
		if w, err := os.OpenFile(filepath.Join(children, "4243.json"), os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = w.Close()
		}
		t.Fatal("the record read waited on a FIFO")
	}
	// The control: the same bytes in a regular file are a record.
	if err := os.WriteFile(filepath.Join(children, "4244.json"), []byte(`{"pid":4244,"start":"1","argv0":"a"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if rec, ok := c.read("4244.json"); !ok || rec.Pid != 4244 {
		t.Errorf("read(regular file) = %+v, %v, want the record", rec, ok)
	}
}

// simTarget makes the children folder of a new run dir, a live process that passes the
// whole record test, and its record file. It returns the fake, the run dir and the target.
func simTarget(t *testing.T) (*procSim, string, reapTarget) {
	t.Helper()
	runDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(runDir, "children"), 0o700); err != nil {
		t.Fatal(err)
	}
	sim := newProcSim(t)
	tg := sim.addVerified(1001, "10", runDir)
	if err := os.WriteFile(filepath.Join(runDir, "children", tg.name), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return sim, runDir, tg
}

// TestReapWaitDropsGroupThatNoLongerVerifies pins the record test inside the grace: a
// group whose leader fails it is dropped at the poll that sees it, long before the
// grace ends. It gets no SIGKILL, its record goes, it is counted in forgotten and never
// as reaped, and the line holds the reference's reason text. The first case is Linux
// row PG05a against 89cb6289: the child replaced itself with another program on the
// SIGTERM, and the reference logged the line 0.2 s after its start. The other reason
// texts come from rows ED04b, ED07, ED10a to ED10d, RP07 and PG05a (the unreadable
// environment). A group that still passes gets the SIGKILL at the end of the grace, as
// the control.
func TestReapWaitDropsGroupThatNoLongerVerifies(t *testing.T) {
	const line = "[process.Registry] group 1001 no longer verifies (%s); nothing more is sent to it\n"
	for _, tc := range []struct {
		name    string
		program string // the program key of the record
		onTerm  func(lp *liveProc)
		reason  string // "" for the control
	}{
		{"the leader became another program", "", func(lp *liveProc) { lp.program = "/f/bin/ustub" },
			`it does not run the recorded program "/n"`},
		{"another program, record with a program key", "/f/bin/sigstub", func(lp *liveProc) { lp.program = "/f/bin/ustub" },
			`it does not run the recorded program "/n" and neither runs nor was handed "/f/bin/sigstub"`},
		{"the environment of the leader cannot be read", "", func(lp *liveProc) {
			lp.state, lp.unreadable = procNotOurs, "environment unreadable: open /proc/1001/environ: permission denied"
		}, "environment unreadable: open /proc/1001/environ: permission denied"},
		{"the leader cannot be read, with no reference text", "", func(lp *liveProc) { lp.state = procNotOurs },
			"process is not in this daemon's namespace"},
		{"the leader lost its run dir entry", "", func(lp *liveProc) { lp.runDir = "" },
			"it was not started by a daemon of this run dir: no CLAUDE_SSH_RUN_DIR names it"},
		{"the leader names another run dir", "", func(lp *liveProc) { lp.runDir = "/run/other" },
			"it belongs to a daemon of another run dir: its CLAUDE_SSH_RUN_DIR names a different one"},
		{"the child mark names another process", "", func(lp *liveProc) { lp.childMark = "7:10" },
			"it was not started by a daemon itself: its CLAUDE_SSH_CHILD does not name its own pid and start (something a child started)"},
		{"the leader left its group", "", func(lp *liveProc) { lp.pgid = 7 },
			"it does not lead its own process group"},
		{"the pid has another start", "", func(lp *liveProc) { lp.startTicks = "11" },
			`it did not start at the recorded "10": the pid has been reused`},
		{"control: the leader still passes", "", func(*liveProc) {}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sim, runDir, tg := simTarget(t)
			tg.rec.Program = tc.program
			sim.onKill[tg.pid] = func(sig syscall.Signal) error {
				switch sig {
				case syscall.SIGTERM:
					tc.onTerm(sim.live[tg.pid]) // the process ignores the SIGTERM and changes
				case syscall.SIGKILL:
					delete(sim.live, tg.pid)
				}
				return nil
			}
			start := sim.now
			var counts reapCounts
			out := captureLog(t, func() { reapTargets(simCtx(t, runDir), []reapTarget{tg}, &counts) })

			if sim.sigCount(syscall.SIGTERM) != 1 {
				t.Errorf("kills = %v, want exactly one SIGTERM", sim.kills)
			}
			if recordExists(runDir, tg.name) {
				t.Error("the record is still there")
			}
			if tc.reason == "" {
				if sim.sigCount(syscall.SIGKILL) != 1 || counts.reapedEscalate != 1 || counts.forgotten != 0 || strings.Contains(out, "no longer verifies") {
					t.Errorf("control: kills = %v, counts = %+v, log = %q, want one SIGKILL, one group in the escalate bucket and no line", sim.kills, counts, out)
				}
				if waited := sim.now.Sub(start); waited < reapGrace {
					t.Errorf("control: the SIGKILL came after %v, want the whole %v grace first", waited, reapGrace)
				}
				return
			}
			if sim.sigCount(syscall.SIGKILL) != 0 {
				t.Errorf("kills = %v, want no SIGKILL", sim.kills)
			}
			if want := fmt.Sprintf(line, tc.reason); !strings.Contains(out, want) {
				t.Errorf("log = %q\nwant it to hold %q", out, want)
			}
			if counts.forgotten != 1 || counts.reapedEscalate != 0 || counts.reapedGrace != 0 || counts.survived != 0 {
				t.Errorf("counts = %+v, want the group counted in forgotten only", counts)
			}
			// The fake clock moves only when the wait sleeps. A drop at the poll that
			// sees the change takes one poll interval, not the grace.
			if waited := sim.now.Sub(start); waited >= reapGrace {
				t.Errorf("the group was dropped after %v, want it dropped at a poll inside the %v grace", waited, reapGrace)
			}
		})
	}
}

// TestReapWaitLiveLeaderIsNotReaped pins a read that takes a live leader for gone. The
// linux reader does that for a process with no address space, which a process is for a
// moment while it replaces its program. Measured on Linux (row PG05a): claustrum logged
// such a leader as reaped in 2 of 10 runs while it was alive. The first read after the
// SIGTERM says gone here, and every later read shows the live process with its new
// program. The group is dropped with the line and is not counted reaped.
func TestReapWaitLiveLeaderIsNotReaped(t *testing.T) {
	sim, runDir, tg := simTarget(t)
	sim.group[tg.pid] = true
	termed, goneReads := false, 0
	inner := readLiveProc
	readLiveProc = func(pid int, wantEnv bool) liveProc {
		if termed && goneReads == 0 {
			goneReads++
			return liveProc{state: procGone}
		}
		return inner(pid, wantEnv)
	}
	sim.onKill[tg.pid] = func(sig syscall.Signal) error {
		if sig == syscall.SIGTERM {
			termed = true
			sim.live[tg.pid].program = "/f/bin/ustub"
		}
		return nil
	}
	var counts reapCounts
	out := captureLog(t, func() { reapTargets(simCtx(t, runDir), []reapTarget{tg}, &counts) })

	if counts.reapedGrace != 0 || counts.reapedEscalate != 0 || counts.forgotten != 1 {
		t.Errorf("counts = %+v, want the live group counted in forgotten, not as reaped", counts)
	}
	if sim.sigCount(syscall.SIGKILL) != 0 {
		t.Errorf("kills = %v, want no SIGKILL for a group whose leader is alive", sim.kills)
	}
	if want := `no longer verifies (it does not run the recorded program "/n")`; !strings.Contains(out, want) {
		t.Errorf("log = %q\nwant it to hold %q", out, want)
	}
}

// TestReapWaitEndedLeaderIsNotDropped pins a full read that fails because the leader
// has just ended: the read says "not ours", and the light read after the settle pause
// shows no process. The group is counted as ended and gets no line. A group that is
// gone is ended. It is never one that no longer verifies.
func TestReapWaitEndedLeaderIsNotDropped(t *testing.T) {
	sim, runDir, tg := simTarget(t)
	termed := false
	inner := readLiveProc
	readLiveProc = func(pid int, wantEnv bool) liveProc {
		switch {
		case !termed:
			return inner(pid, wantEnv)
		case wantEnv:
			return liveProc{state: procNotOurs} // the read that the end of the process broke
		default:
			return liveProc{state: procGone}
		}
	}
	sim.onKill[tg.pid] = func(syscall.Signal) error { termed = true; return nil }
	var counts reapCounts
	out := captureLog(t, func() { reapTargets(simCtx(t, runDir), []reapTarget{tg}, &counts) })

	if counts.reapedGrace != 1 || counts.forgotten != 0 || len(sim.kills) != 1 {
		t.Errorf("counts = %+v, kills = %v, want one SIGTERM and the group counted as ended", counts, sim.kills)
	}
	if strings.Contains(out, "no longer verifies") {
		t.Errorf("an ended group was logged as one that no longer verifies:\n%s", out)
	}
}

// TestReapTargetsLeaderGoneMembersLinger pins the cleanup of row RP05a: the leader
// exits on the SIGTERM and members of its group stay. The leader is still gone after
// the settle pause, so the group gets the SIGKILL, and the target is counted as ended
// in the grace (89cb6289 and claustrum, Linux row RP05a: SIGTERM, a signal 0 that
// answers, SIGKILL).
func TestReapTargetsLeaderGoneMembersLinger(t *testing.T) {
	sim, runDir, tg := simTarget(t)
	sim.onKill[tg.pid] = func(sig syscall.Signal) error {
		if sig == syscall.SIGTERM {
			delete(sim.live, tg.pid) // the leader exits
			sim.group[tg.pid] = true // its members ignore the SIGTERM
		}
		return nil
	}
	start := sim.now
	var counts reapCounts
	reapTargets(simCtx(t, runDir), []reapTarget{tg}, &counts)

	want := []killRec{{tg.pid, syscall.SIGTERM}, {tg.pid, syscall.SIGKILL}}
	if len(sim.kills) != 2 || sim.kills[0] != want[0] || sim.kills[1] != want[1] {
		t.Errorf("signals = %v, want %v", sim.kills, want)
	}
	if counts.reapedGrace != 1 || counts.forgotten != 0 || counts.reapedEscalate != 0 {
		t.Errorf("counts = %+v, want the group counted as ended in the grace", counts)
	}
	if waited := sim.now.Sub(start); waited < reapSettle || waited >= reapGrace {
		t.Errorf("the group SIGKILL came after %v, want it after the %v settle pause and inside the grace", waited, reapSettle)
	}
	if recordExists(runDir, tg.name) {
		t.Error("the record is still there")
	}
}

// TestReapTargetsLeaderReadsGoneOnceBeforeSIGKILL covers a leader that reads as gone
// once, at the test before the SIGKILL, and is alive after the settle pause. It takes
// the record test again. A leader that still passes gets the SIGKILL. A leader that
// became another program is dropped with the line and gets no SIGKILL.
func TestReapTargetsLeaderReadsGoneOnceBeforeSIGKILL(t *testing.T) {
	for _, changed := range []bool{false, true} {
		name := "still the recorded child"
		if changed {
			name = "became another program"
		}
		t.Run(name, func(t *testing.T) {
			sim, runDir, tg := simTarget(t)
			sim.diesOn(tg.pid, syscall.SIGKILL)
			start := sim.now
			armed, goneOnce := true, false
			reapNow = func() time.Time {
				if armed && sim.now.Sub(start) >= reapGrace {
					armed, goneOnce = false, true
					if changed {
						sim.live[tg.pid].program = "/f/bin/ustub"
					}
				}
				return sim.now
			}
			inner := readLiveProc
			readLiveProc = func(pid int, wantEnv bool) liveProc {
				if goneOnce {
					goneOnce = false
					return liveProc{state: procGone}
				}
				return inner(pid, wantEnv)
			}
			var counts reapCounts
			out := captureLog(t, func() { reapTargets(simCtx(t, runDir), []reapTarget{tg}, &counts) })

			if changed {
				if sim.sigCount(syscall.SIGKILL) != 0 || counts.forgotten != 1 || !strings.Contains(out, `no longer verifies (it does not run the recorded program "/n")`) {
					t.Errorf("kills = %v, counts = %+v, log = %q, want no SIGKILL, the group in forgotten and the line", sim.kills, counts, out)
				}
				return
			}
			if sim.sigCount(syscall.SIGKILL) != 1 || counts.reapedEscalate != 1 || counts.reapedGrace != 0 || counts.forgotten != 0 {
				t.Errorf("kills = %v, counts = %+v, want one SIGKILL and the group in the escalate bucket", sim.kills, counts)
			}
		})
	}
}

// TestReapWaitReadsThatDisagree covers a target whose two reads disagree twice: the
// full read says gone and the light read shows a live process. The target is dropped
// and gets no signal. The reason text is claustrum's own.
func TestReapWaitReadsThatDisagree(t *testing.T) {
	sim, runDir, tg := simTarget(t)
	termed := false
	inner := readLiveProc
	readLiveProc = func(pid int, wantEnv bool) liveProc {
		if termed && wantEnv {
			return liveProc{state: procGone}
		}
		return inner(pid, wantEnv)
	}
	sim.onKill[tg.pid] = func(syscall.Signal) error { termed = true; return nil }
	var counts reapCounts
	out := captureLog(t, func() { reapTargets(simCtx(t, runDir), []reapTarget{tg}, &counts) })

	if counts.forgotten != 1 || counts.reapedGrace != 0 || len(sim.kills) != 1 {
		t.Errorf("counts = %+v, kills = %v, want one SIGTERM and the group in forgotten only", counts, sim.kills)
	}
	if want := "no longer verifies (its state changed between two reads)"; !strings.Contains(out, want) {
		t.Errorf("log = %q\nwant it to hold %q", out, want)
	}
}

// TestReapTargetsLeaderGoneBeforeSIGKILL covers a leader that ends between the last poll
// of the grace and the test before the SIGKILL: it is counted in the grace bucket, a
// member that lingers in its group gets the group SIGKILL, and the record goes.
func TestReapTargetsLeaderGoneBeforeSIGKILL(t *testing.T) {
	sim, runDir, tg := simTarget(t)
	sim.group[tg.pid] = true
	// The wait reads the clock right after its last poll. The leader ends then.
	start := sim.now
	reapNow = func() time.Time {
		if sim.now.Sub(start) >= reapGrace {
			delete(sim.live, tg.pid)
		}
		return sim.now
	}
	var counts reapCounts
	reapTargets(simCtx(t, runDir), []reapTarget{tg}, &counts)

	if counts.reapedGrace != 1 || counts.reapedEscalate != 0 || counts.forgotten != 0 || counts.survived != 0 {
		t.Errorf("counts = %+v, want the group in the grace bucket only", counts)
	}
	if sim.sigCount(syscall.SIGKILL) != 1 {
		t.Errorf("kills = %v, want one SIGKILL for the member that lingers", sim.kills)
	}
	if recordExists(runDir, tg.name) {
		t.Error("the record is still there")
	}
}

// TestReapTargetsChangedBeforeSIGKILL covers a leader that passes every poll of the
// grace and changes right after the last one: the test before the SIGKILL sees it, the
// group gets no SIGKILL, and it is dropped with the line.
func TestReapTargetsChangedBeforeSIGKILL(t *testing.T) {
	sim, runDir, tg := simTarget(t)
	start := sim.now
	reapNow = func() time.Time {
		if lp := sim.live[tg.pid]; lp != nil && sim.now.Sub(start) >= reapGrace {
			lp.program = "/f/bin/ustub"
		}
		return sim.now
	}
	var counts reapCounts
	out := captureLog(t, func() { reapTargets(simCtx(t, runDir), []reapTarget{tg}, &counts) })

	if sim.sigCount(syscall.SIGKILL) != 0 || counts.forgotten != 1 || counts.reapedEscalate != 0 {
		t.Errorf("kills = %v, counts = %+v, want no SIGKILL and the group in forgotten", sim.kills, counts)
	}
	if want := `no longer verifies (it does not run the recorded program "/n")`; !strings.Contains(out, want) {
		t.Errorf("log = %q\nwant it to hold %q", out, want)
	}
}

// TestReapOrphansUnreadableRecordAndRunDir covers two sweeps that read nothing. A
// record of mode 000 is removed with no signal (89cb6289, Linux row ED16d). A run dir
// that cannot be opened ends the sweep at once.
func TestReapOrphansUnreadableRecordAndRunDir(t *testing.T) {
	sim := newProcSim(t)
	reapOrphans(filepath.Join(t.TempDir(), "gone"), "inst-self")

	if os.Geteuid() == 0 {
		t.Skip("root can read a file of mode 000")
	}
	runDir := t.TempDir()
	const pid = 800000031
	name := writeRec(t, runDir, orphanRecord(pid, "/f/bin/sigstub", ""))
	if err := os.Chmod(filepath.Join(runDir, "children", name), 0o000); err != nil {
		t.Fatal(err)
	}
	sim.live[pid] = &liveProc{state: procAlive, startTicks: "9000", pgid: pid,
		program: "/f/bin/sigstub", runDir: runDir, childMark: strconv.Itoa(pid) + ":9000"}

	reapOrphans(runDir, "inst-self")

	if len(sim.kills) != 0 || recordExists(runDir, name) {
		t.Errorf("kills = %v, record exists = %v, want no signal and the record removed", sim.kills, recordExists(runDir, name))
	}
}

// TestReapOrphansChildrenSymlink pins that the sweep never leaves the run dir. The
// children entry is a symlink to a folder that holds a valid record of a live process,
// which passes every test of the sweep. The sweep sends no signal, removes nothing and
// logs the reaping-nothing line, for a folder outside the run dir and for one inside
// it (f6010b97 and 89cb6289, Linux rows SYa and SYb). Before, the sweep listed the
// folder by its path, followed the symlink and sent SIGTERM.
func TestReapOrphansChildrenSymlink(t *testing.T) {
	if node, _ := ownReapIdentity(); node == "" {
		t.Skip("no node identity on this host")
	}
	for _, where := range []string{"outside the run dir", "inside the run dir"} {
		t.Run(where, func(t *testing.T) {
			runDir := t.TempDir()
			holder := t.TempDir()
			link := holder
			if where == "inside the run dir" {
				holder = filepath.Join(runDir, "kids")
				if err := os.Mkdir(holder, 0o700); err != nil {
					t.Fatal(err)
				}
				link = "kids"
			}
			const pid = 800000021
			// The record sits in <holder>/children/<pid>.json. The children entry of
			// the run dir is a symlink to that folder.
			name := writeRec(t, holder, orphanRecord(pid, "/f/bin/sigstub", ""))
			if err := os.Symlink(filepath.Join(link, "children"), filepath.Join(runDir, "children")); err != nil {
				t.Fatal(err)
			}
			sim := newProcSim(t)
			sim.live[pid] = &liveProc{state: procAlive, startTicks: "9000", pgid: pid,
				program: "/f/bin/sigstub", runDir: runDir, childMark: strconv.Itoa(pid) + ":9000"}
			sim.diesOn(pid, syscall.SIGTERM)

			out := captureLog(t, func() { reapOrphans(runDir, "inst-self") })

			if want := "[process.Registry] " + runDir + "/children is not a directory; reaping nothing\n"; !strings.Contains(out, want) {
				t.Errorf("log = %q\nwant it to hold %q", out, want)
			}
			if len(sim.kills) != 0 {
				t.Errorf("the sweep sent signals through a children symlink: %v", sim.kills)
			}
			if !recordExists(holder, name) {
				t.Error("the sweep removed a record through a children symlink")
			}
			if fi, err := os.Lstat(filepath.Join(runDir, "children")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
				t.Errorf("the children symlink changed: %v, %v", fi, err)
			}
		})
	}
}

// TestVerifyOrphan walks the whole verdict tree: each guard returns its skip and the all-
// pass record returns a reap. A mutant that drops any single guard flips exactly one row.
func TestVerifyOrphan(t *testing.T) {
	const runDir = "/run/c0ffee01"
	const pid = 424242
	// A live process that passes every check, so each case below removes exactly one.
	pass := liveProc{
		state:      procAlive,
		startTicks: "9000",
		pgid:       pid,
		program:    "/usr/bin/node",
		runDir:     runDir,
		childMark:  strconv.Itoa(pid) + ":9000",
	}
	rec := childRecord{Pid: pid, Start: "9000", Argv0: "/usr/bin/node"}
	// Pick owner/parent/pgid values distinct from pid for the all-pass rows.
	const ownDaemon, ownParent, ownPgid = 10, 11, 12

	cases := []struct {
		name        string
		rec         childRecord
		live        liveProc
		ownDaemon   int
		ownParent   int
		ownPgid     int
		wantVerdict int
		wantReason  string
	}{
		{"pid below 2", childRecord{Pid: 1, Start: "1", Argv0: "x"}, pass, ownDaemon, ownParent, ownPgid, verdictSkip, "pid is out of range"},
		{"pid is this daemon", childRecord{Pid: ownDaemon, Start: "1", Argv0: "x"}, pass, ownDaemon, ownParent, ownPgid, verdictSkip, "pid is this daemon"},
		{"pid is parent", childRecord{Pid: ownParent, Start: "1", Argv0: "x"}, pass, ownDaemon, ownParent, ownPgid, verdictSkip, "pid is this daemon's parent or process group"},
		{"pid is pgroup", childRecord{Pid: ownPgid, Start: "1", Argv0: "x"}, pass, ownDaemon, ownParent, ownPgid, verdictSkip, "pid is this daemon's parent or process group"},
		{"no start", childRecord{Pid: pid, Start: "", Argv0: "x"}, pass, ownDaemon, ownParent, ownPgid, verdictSkip, "record has no start time to verify against"},
		{"no program", childRecord{Pid: pid, Start: "1", Argv0: ""}, pass, ownDaemon, ownParent, ownPgid, verdictSkip, "record names no program to verify against"},
		{"live gone", rec, liveProc{state: procGone}, ownDaemon, ownParent, ownPgid, verdictGone, ""},
		{"live not ours", rec, liveProc{state: procNotOurs}, ownDaemon, ownParent, ownPgid, verdictPassthrough, "process is not in this daemon's namespace"},
		{"pid reused", rec, liveProc{state: procAlive, startTicks: "8888", pgid: pid, program: "/usr/bin/node", runDir: runDir, childMark: strconv.Itoa(pid) + ":8888"}, ownDaemon, ownParent, ownPgid, verdictSkip, "the pid was reused"},
		{"not group leader", rec, liveProc{state: procAlive, startTicks: "9000", pgid: 7, program: "/usr/bin/node", runDir: runDir, childMark: strconv.Itoa(pid) + ":9000"}, ownDaemon, ownParent, ownPgid, verdictSkip, "process is not its own group leader"},
		{"wrong program", rec, liveProc{state: procAlive, startTicks: "9000", pgid: pid, program: "/bin/sh", runDir: runDir, childMark: strconv.Itoa(pid) + ":9000"}, ownDaemon, ownParent, ownPgid, verdictSkip, "process is not running the recorded program"},
		{"no run-dir marker", rec, liveProc{state: procAlive, startTicks: "9000", pgid: pid, program: "/usr/bin/node", runDir: "", childMark: strconv.Itoa(pid) + ":9000"}, ownDaemon, ownParent, ownPgid, verdictSkip, "process has no run-dir marker"},
		{"other run dir", rec, liveProc{state: procAlive, startTicks: "9000", pgid: pid, program: "/usr/bin/node", runDir: "/run/other", childMark: strconv.Itoa(pid) + ":9000"}, ownDaemon, ownParent, ownPgid, verdictSkip, "process belongs to another run dir"},
		{"child mark mismatch", rec, liveProc{state: procAlive, startTicks: "9000", pgid: pid, program: "/usr/bin/node", runDir: runDir, childMark: "999:9000"}, ownDaemon, ownParent, ownPgid, verdictSkip, "process was not started directly by a daemon"},
		{"empty live program", rec, liveProc{state: procAlive, startTicks: "9000", pgid: pid, program: "", runDir: runDir, childMark: strconv.Itoa(pid) + ":9000"}, ownDaemon, ownParent, ownPgid, verdictSkip, "process is not running the recorded program"},
		{"reap when all pass", rec, pass, ownDaemon, ownParent, ownPgid, verdictReap, ""},
		{"reap by basename match", childRecord{Pid: pid, Start: "9000", Argv0: "node"}, pass, ownDaemon, ownParent, ownPgid, verdictReap, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeLiveProcs(t, map[int]liveProc{tc.rec.Pid: tc.live})
			v, reason := verifyOrphan(tc.rec, runDir, tc.ownDaemon, tc.ownParent, tc.ownPgid)
			if v != tc.wantVerdict || reason != tc.wantReason {
				t.Errorf("verdict/reason = %d/%q, want %d/%q", v, reason, tc.wantVerdict, tc.wantReason)
			}
		})
	}
}

// TestSettleThenVerdictReportsGone proves the skip path re-reads /proc after a short settle and
// reports gone when the process exited in the meantime, rather than logging a skip for a
// process that just raced away.
func TestSettleThenVerdictReportsGone(t *testing.T) {
	const pid = 555
	calls := 0
	oldRead, oldSleep := readLiveProc, reapSleep
	t.Cleanup(func() { readLiveProc, reapSleep = oldRead, oldSleep })
	reapSleep = func(time.Duration) {}
	readLiveProc = func(p int, wantEnv bool) liveProc {
		calls++
		if calls == 1 { // the verify read: alive but failing a check
			return liveProc{state: procAlive, startTicks: "1", pgid: 999, program: "x", runDir: "/run/x", childMark: "1:1"}
		}
		return liveProc{state: procGone} // the settle re-read: now gone
	}
	rec := childRecord{Pid: pid, Start: "1", Argv0: "x"}
	v, _ := verifyOrphan(rec, "/run/x", 1, 2, 3)
	if v != verdictGone {
		t.Errorf("verdict = %d, want verdictGone after the settle re-read", v)
	}
}

// TestSameLeader covers the pre-kill re-check: reused pid, no longer group leader, gone,
// and still safe to signal.
func TestSameLeader(t *testing.T) {
	const pid = 700
	cases := []struct {
		name    string
		recStrt string
		live    liveProc
		want    int
	}{
		{"reused", "100", liveProc{state: procAlive, startTicks: "200", pgid: pid}, verdictSkip},
		{"gone", "100", liveProc{state: procGone}, verdictGone},
		{"not leader", "100", liveProc{state: procAlive, startTicks: "100", pgid: 9}, verdictSkip},
		{"ok", "100", liveProc{state: procAlive, startTicks: "100", pgid: pid}, verdictReap},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeLiveProcs(t, map[int]liveProc{pid: tc.live})
			if got := sameLeader(pid, tc.recStrt); got != tc.want {
				t.Errorf("sameLeader = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestDaemonAlive covers the owner-liveness gate that decides whether a child is an orphan.
func TestDaemonAlive(t *testing.T) {
	const dpid = 800
	const ownPid = 42
	cases := []struct {
		name  string
		pid   int
		start string
		live  liveProc
		want  bool
	}{
		{"pid below 2", 1, "", liveProc{state: procAlive}, false},
		{"pid is us", ownPid, "", liveProc{state: procAlive, startTicks: "5"}, false},
		{"gone", dpid, "", liveProc{state: procGone}, false},
		{"alive no recorded start", dpid, "", liveProc{state: procAlive, startTicks: "5"}, true},
		{"alive matching start", dpid, "5", liveProc{state: procAlive, startTicks: "5"}, true},
		{"alive mismatched start", dpid, "5", liveProc{state: procAlive, startTicks: "6"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeLiveProcs(t, map[int]liveProc{tc.pid: tc.live})
			if got := daemonAlive(tc.pid, tc.start, ownPid); got != tc.want {
				t.Errorf("daemonAlive = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestReapOrphansForgetsBadRecords proves a record that will not parse, names a bad pid,
// or sits at a mismatched file name is dropped without a kill.
func TestReapOrphansForgetsBadRecords(t *testing.T) {
	runDir := t.TempDir()
	dir := filepath.Join(runDir, "children")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "12.json"), []byte("{bad json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A valid record whose pid does not match its file name (a leftover temp name).
	tempName := "424242.111.json"
	if err := os.WriteFile(filepath.Join(dir, tempName), []byte(`{"pid":424242,"start":"1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// A non-json file is ignored entirely (left in place, never read).
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	fakeLiveProcs(t, map[int]liveProc{})
	oldKill := killGroup
	t.Cleanup(func() { killGroup = oldKill })
	killed := 0
	killGroup = func(int, syscall.Signal) error { killed++; return nil }

	reapOrphans(runDir, "inst")

	if killed != 0 {
		t.Errorf("killGroup called %d times for bad records; want 0", killed)
	}
	if recordExists(runDir, "12.json") || recordExists(runDir, tempName) {
		t.Error("a malformed or mismatched record survived; both must be forgotten")
	}
	if !recordExists(runDir, "notes.txt") {
		t.Error("a non-.json file was removed; the sweep must ignore it")
	}
}

// TestReapTargetsTwoPhase proves the escalation: a group that survives the grace after
// SIGTERM is sent SIGKILL and, when it then exits, is counted reaped in the escalate bucket;
// one that outlives SIGKILL is counted survived. The reaped group's record is forgotten, but
// the survivor's record is KEPT (the reaper does not forget a group it could not end).
func TestReapTargetsTwoPhase(t *testing.T) {
	runDir := t.TempDir()
	dir := filepath.Join(runDir, "children")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sim := newProcSim(t)
	// Each target still passes the whole record test at the end of the grace, so each
	// one gets the SIGKILL.
	t1 := sim.addVerified(1001, "10", runDir)
	t2 := sim.addVerified(1002, "20", runDir)
	for _, tt := range []reapTarget{t1, t2} {
		if err := os.WriteFile(filepath.Join(dir, tt.name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sim.diesOn(t1.pid, syscall.SIGKILL) // survives SIGTERM + the grace, dies to the escalation
	// t2 has no death policy: it outlives SIGKILL (a zombie or an uninterruptible sleep).
	var counts reapCounts
	reapTargets(simCtx(t, runDir), []reapTarget{t1, t2}, &counts)

	if sim.sigCount(syscall.SIGTERM) != 2 || sim.sigCount(syscall.SIGKILL) != 2 {
		t.Errorf("signals: %d SIGTERM, %d SIGKILL; want 2 and 2", sim.sigCount(syscall.SIGTERM), sim.sigCount(syscall.SIGKILL))
	}
	if counts.signalled != 2 || counts.reapedEscalate != 1 || counts.survived != 1 {
		t.Errorf("counts signalled=%d reapedEscalate=%d survived=%d, want 2, 1 and 1",
			counts.signalled, counts.reapedEscalate, counts.survived)
	}
	if counts.reapedGrace != 0 || counts.forgotten != 0 {
		t.Errorf("counts reapedGrace=%d forgotten=%d, want 0 and 0", counts.reapedGrace, counts.forgotten)
	}
	if recordExists(runDir, t1.name) {
		t.Error("the reaped target's record survived; it must be forgotten")
	}
	if !recordExists(runDir, t2.name) {
		t.Error("the survivor's record was forgotten; a group the reaper could not end must be KEPT")
	}
}

// TestReapOrphansForgetOnlyIsSilent proves the summary gate excludes forgotten: a sweep that
// only forgets stale records (nothing signalled, left alive, or kept) logs no summary line,
// matching reference build 19f30c46 (VM-confirmed). A mutant that puts forgotten back in the
// gate logs a summary here and fails this test.
func TestReapOrphansForgetOnlyIsSilent(t *testing.T) {
	runDir := t.TempDir()
	dir := filepath.Join(runDir, "children")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// An unparseable record is forgotten without any signal or keep, so only forgotten is
	// non-zero after the sweep.
	if err := os.WriteFile(filepath.Join(dir, "12.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	fakeLiveProcs(t, map[int]liveProc{})
	out := captureLog(t, func() { reapOrphans(runDir, "inst") })
	if strings.Contains(out, "orphan sweep") {
		t.Errorf("a forget-only sweep logged a summary; it must be silent:\n%s", out)
	}
	if recordExists(runDir, "12.json") {
		t.Error("the malformed record was not forgotten")
	}
}

// TestReapTargetsPreKillReuse proves the sameLeader re-check stops a signal to a pid that
// was reused or exited between the sweep and the kill.
func TestReapTargetsPreKillReuse(t *testing.T) {
	runDir := t.TempDir()
	dir := filepath.Join(runDir, "children")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	reused := reapTarget{pid: 2001, start: "10", name: "2001.json"}
	gone := reapTarget{pid: 2002, start: "20", name: "2002.json"}
	sigfail := reapTarget{pid: 2003, start: "30", name: "2003.json"}
	for _, tt := range []reapTarget{reused, gone, sigfail} {
		if err := os.WriteFile(filepath.Join(dir, tt.name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sim := newProcSim(t)
	sim.live[reused.pid] = &liveProc{state: procAlive, startTicks: "999", pgid: reused.pid} // start changed => reused
	// gone.pid is absent from the sim, so it reads gone.
	sim.add(sigfail.pid, "30")
	sim.onKill[sigfail.pid] = func(syscall.Signal) error { return syscall.ESRCH } // the signal fails
	var counts reapCounts
	reapTargets(simCtx(t, runDir), []reapTarget{reused, gone, sigfail}, &counts)

	// A reused pid and a gone pid are never signaled; sigfail is signaled once and its
	// error stops it there.
	if sim.signaled(reused.pid) || sim.signaled(gone.pid) {
		t.Errorf("a reused or gone pid was signaled: kills=%v", sim.kills)
	}
	if recordExists(runDir, reused.name) || recordExists(runDir, gone.name) || recordExists(runDir, sigfail.name) {
		t.Error("a handled target's record survived; all must be forgotten")
	}
	// The phase-1 already-gone target lands in the grace bucket; the reused and signal-failed
	// targets are forgotten. A mutant that mis-buckets the phase-1 gone case flips these.
	if counts.signalled != 3 || counts.reapedGrace != 1 || counts.forgotten != 2 {
		t.Errorf("counts signalled=%d reapedGrace=%d forgotten=%d, want 3, 1 and 2",
			counts.signalled, counts.reapedGrace, counts.forgotten)
	}
}

// TestReapOrphansNoop proves the two early returns: an empty run dir and a missing children
// dir both do nothing (and never kill).
func TestReapOrphansNoop(t *testing.T) {
	oldKill := killGroup
	t.Cleanup(func() { killGroup = oldKill })
	killGroup = func(int, syscall.Signal) error { t.Fatal("killGroup called in a no-op sweep"); return nil }
	reapOrphans("", "inst")          // no run dir
	reapOrphans(t.TempDir(), "inst") // run dir with no children/ subdir
}

// TestReadChildRecordMissing covers the missing-file arm of the record read.
func TestReadChildRecordMissing(t *testing.T) {
	runDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(runDir, "children"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, ok := simCtx(t, runDir).read("nope.json"); ok {
		t.Error("the record read reported ok for a missing file")
	}
}

// TestReapWaitDropsReusedAndCleansGroup covers the two lifecycle guards inside the wait: a
// pid reused during the grace is dropped without a signal (never SIGKILLed), and a target
// whose leader exits while a group member lingers has its group SIGKILLed. A target that
// stays the same live leader is returned as still pending at the deadline.
func TestReapWaitDropsReusedAndCleansGroup(t *testing.T) {
	runDir := t.TempDir()
	dir := filepath.Join(runDir, "children")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sim := newProcSim(t)
	staysAlive := sim.addVerified(3001, "10", runDir) // the recorded child throughout
	reused := reapTarget{pid: 3002, start: "20", name: "3002.json", rec: childRecord{Pid: 3002, Start: "20", Argv0: "/n"}}
	groupSurvivor := reapTarget{pid: 3003, start: "30", name: "3003.json", rec: childRecord{Pid: 3003, Start: "30", Argv0: "/n"}}
	for _, tt := range []reapTarget{staysAlive, reused, groupSurvivor} {
		if err := os.WriteFile(filepath.Join(dir, tt.name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sim.live[reused.pid] = &liveProc{state: procAlive, startTicks: "777", pgid: reused.pid} // start changed => reused
	// groupSurvivor.pid is absent (leader gone), but its group still has a member.
	sim.group[groupSurvivor.pid] = true

	var counts reapCounts
	pending := reapWait(simCtx(t, runDir), []reapTarget{staysAlive, reused, groupSurvivor}, reapGrace, &counts.reapedGrace, &counts, true)

	if len(pending) != 1 || pending[0].pid != staysAlive.pid {
		t.Errorf("pending = %v, want only the live leader %d", pending, staysAlive.pid)
	}
	if sim.signaled(reused.pid) {
		t.Error("a pid reused during the grace was signaled; it must be dropped")
	}
	if sim.sigCount(syscall.SIGKILL) != 1 || !sim.signaled(groupSurvivor.pid) {
		t.Errorf("group survivor not cleaned up: kills=%v", sim.kills)
	}
	if counts.reapedGrace != 1 || counts.forgotten != 1 {
		t.Errorf("counts reapedGrace=%d forgotten=%d, want 1 and 1", counts.reapedGrace, counts.forgotten)
	}
	if recordExists(runDir, reused.name) || recordExists(runDir, groupSurvivor.name) {
		t.Error("a resolved target's record survived; it must be forgotten")
	}
	if !recordExists(runDir, staysAlive.name) {
		t.Error("a still-pending target's record was forgotten early")
	}
}

// TestReapWaitSkipsGroupKillOnLeaderReuse proves the group cleanup re-confirms the leader
// is still gone right before the SIGKILL: if the pid was recycled as a new group leader
// between the poll's first read and the signal, the group is NOT SIGKILLed. The target
// is then tested again, fails as a reused pid, and is dropped. It is not counted reaped,
// because a live process holds its pid.
func TestReapWaitSkipsGroupKillOnLeaderReuse(t *testing.T) {
	runDir := t.TempDir()
	dir := filepath.Join(runDir, "children")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	tg := reapTarget{pid: 5001, start: "10", name: "5001.json", rec: childRecord{Pid: 5001, Start: "10", Argv0: "/n"}}
	if err := os.WriteFile(filepath.Join(dir, tg.name), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldRead, oldKill, oldGroup, oldNow, oldSleep := readLiveProc, killGroup, groupAlive, reapNow, reapSleep
	t.Cleanup(func() {
		readLiveProc, killGroup, groupAlive, reapNow, reapSleep = oldRead, oldKill, oldGroup, oldNow, oldSleep
	})
	reads := 0
	readLiveProc = func(pid int, wantEnv bool) liveProc {
		reads++
		if reads == 1 {
			return liveProc{state: procGone} // the test of the poll: the leader is gone
		}
		return liveProc{state: procAlive, startTicks: "999", pgid: tg.pid} // re-check: pid reused as a new leader
	}
	killed := 0
	killGroup = func(int, syscall.Signal) error { killed++; return nil }
	groupAlive = func(int) bool { return true } // a group with this pgid exists (the reused leader's)
	reapNow = time.Now
	reapSleep = func(time.Duration) {}

	var counts reapCounts
	var pending []reapTarget
	out := captureLog(t, func() {
		pending = reapWait(simCtx(t, runDir), []reapTarget{tg}, reapGrace, &counts.reapedGrace, &counts, true)
	})
	if killed != 0 {
		t.Errorf("group SIGKILL sent despite the leader pid being reused; want 0, got %d", killed)
	}
	if len(pending) != 0 || counts.reapedGrace != 0 || counts.forgotten != 1 {
		t.Errorf("pending=%v reapedGrace=%d forgotten=%d, want [], 0 and 1 (a pid that a live process holds is dropped, not reaped)", pending, counts.reapedGrace, counts.forgotten)
	}
	if want := `[process.Registry] group 5001 no longer verifies (it did not start at the recorded "10": the pid has been reused); nothing more is sent to it`; !strings.Contains(out, want) {
		t.Errorf("log = %q\nwant it to hold %q", out, want)
	}
	if recordExists(runDir, tg.name) {
		t.Error("record survived; a dropped target must be forgotten")
	}
}
