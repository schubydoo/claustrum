//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeLiveProcs seams readLiveProc at a fixed table keyed by pid; an unlisted pid reads
// as gone. It also silences reapSleep so settleThenVerdict's 50ms settle does not slow the test.
func fakeLiveProcs(t *testing.T, table map[int]liveProc) {
	t.Helper()
	oldRead, oldSleep := readLiveProc, reapSleep
	t.Cleanup(func() { readLiveProc, reapSleep = oldRead, oldSleep })
	readLiveProc = func(pid int, wantEnv bool) liveProc {
		if lp, ok := table[pid]; ok {
			return lp
		}
		return liveProc{state: procGone}
	}
	reapSleep = func(time.Duration) {}
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

// TestEarlierBootOfThisMachine pins the machine/boot comparison.
func TestEarlierBootOfThisMachine(t *testing.T) {
	const host = "machine-id:abc"
	cases := []struct {
		name                               string
		recHost, recNode, ownHost, ownNode string
		want                               bool
	}{
		{"same machine earlier boot", host, "bootA/ns1", host, "bootB/ns1", true},
		{"same machine same boot", host, "bootA/ns1", host, "bootA/ns2", false},
		{"different machine", "machine-id:xyz", "bootA/ns1", host, "bootB/ns1", false},
		{"empty host", "", "bootA/ns1", host, "bootB/ns1", false},
		{"unparseable node", host, "bootA", host, "bootB", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := earlierBootOfThisMachine(tc.recHost, tc.recNode, tc.ownHost, tc.ownNode); got != tc.want {
				t.Errorf("earlierBootOfThisMachine = %v, want %v", got, tc.want)
			}
		})
	}
}

// writeRec writes one <runDir>/children/<pid>.json record and returns its file name.
func writeRec(t *testing.T, runDir string, rec childRecord) string {
	t.Helper()
	if err := writeChildRecord(runDir, rec); err != nil {
		t.Fatalf("writeChildRecord: %v", err)
	}
	return strconv.Itoa(rec.Pid) + ".json"
}

func recordExists(runDir, name string) bool {
	_, err := os.Stat(filepath.Join(runDir, "children", name))
	return err == nil
}

// TestReapOrphansGate is the end-to-end sweep: our own child, a live-owner child, a real
// orphan, a gone record, a foreign-fresh record and a foreign-stale record all get the
// right treatment. Every /proc read and kill is seamed, so nothing real is signaled.
func TestReapOrphansGate(t *testing.T) {
	runDir := t.TempDir()
	const ownInstance = "inst-self"
	ownNode := nodeID()
	ownHost := "machine-id:" + machineID()
	if ownNode == "" {
		t.Skip("nodeID unavailable on this host")
	}

	const orphanPid = 424242
	const deadDaemon = 999999
	orphanName := writeRec(t, runDir, childRecord{
		Pid: orphanPid, Node: ownNode, Host: ownHost, Instance: "inst-old",
		DaemonPid: deadDaemon, DaemonStart: "1", Argv0: "/usr/bin/node", Start: "9000", At: time.Now().UnixMilli(),
	})
	mineName := writeRec(t, runDir, childRecord{
		Pid: 111, Node: ownNode, Host: ownHost, Instance: ownInstance,
		DaemonPid: os.Getpid(), Argv0: "/x", Start: "1", At: time.Now().UnixMilli(),
	})
	liveOwnerName := writeRec(t, runDir, childRecord{
		Pid: 222, Node: ownNode, Host: ownHost, Instance: "inst-other",
		DaemonPid: 333, DaemonStart: "50", Argv0: "/y", Start: "2", At: time.Now().UnixMilli(),
	})
	goneName := writeRec(t, runDir, childRecord{
		Pid: 444, Node: ownNode, Host: ownHost, Instance: "inst-old",
		DaemonPid: deadDaemon, DaemonStart: "1", Argv0: "/z", Start: "3", At: time.Now().UnixMilli(),
	})
	foreignFreshName := writeRec(t, runDir, childRecord{
		Pid: 555, Node: "otherboot/ns", Host: "machine-id:other", Instance: "inst-far",
		DaemonPid: 6, Argv0: "/a", Start: "4", At: time.Now().UnixMilli(),
	})
	foreignStaleName := writeRec(t, runDir, childRecord{
		Pid: 666, Node: "otherboot/ns", Host: "machine-id:other", Instance: "inst-far",
		DaemonPid: 6, Argv0: "/b", Start: "5", At: time.Now().Add(-40 * 24 * time.Hour).UnixMilli(),
	})
	// A verify-skip orphan: dead owner, same node, but the live process is not its own
	// group leader, so verifyOrphan returns a skip and the record is forgotten.
	skipName := writeRec(t, runDir, childRecord{
		Pid: 777, Node: ownNode, Host: ownHost, Instance: "inst-old",
		DaemonPid: deadDaemon, DaemonStart: "1", Argv0: "/n", Start: "7", At: time.Now().UnixMilli(),
	})
	// A passthrough orphan: the live process disowns this daemon (another namespace), so
	// the record is kept for a daemon that owns it.
	passthroughName := writeRec(t, runDir, childRecord{
		Pid: 888, Node: ownNode, Host: ownHost, Instance: "inst-old",
		DaemonPid: deadDaemon, DaemonStart: "1", Argv0: "/n", Start: "8", At: time.Now().UnixMilli(),
	})
	// An earlier-boot record of THIS machine (same host, different boot id): forgotten.
	_, ownNS, _ := strings.Cut(ownNode, "/")
	earlierBootName := writeRec(t, runDir, childRecord{
		Pid: 990, Node: "oldboot/" + ownNS, Host: ownHost, Instance: "inst-old",
		DaemonPid: 6, Argv0: "/n", Start: "9", At: time.Now().UnixMilli(),
	})

	fakeLiveProcs(t, map[int]liveProc{
		orphanPid: {state: procAlive, startTicks: "9000", pgid: orphanPid, program: "/usr/bin/node", runDir: runDir, childMark: strconv.Itoa(orphanPid) + ":9000"},
		333:       {state: procAlive, startTicks: "50"},                                        // the live owning daemon
		777:       {state: procAlive, startTicks: "7", pgid: 5, program: "/n", runDir: runDir}, // not group leader -> skip
		888:       {state: procNotOurs},                                                        // disowns us -> passthrough
		// deadDaemon and pid 444 are unlisted, so they read as gone.
	})
	var killed []int
	oldKill, oldWait := killGroup, reapWaitExit
	t.Cleanup(func() { killGroup, reapWaitExit = oldKill, oldWait })
	killGroup = func(pid int, sig syscall.Signal) error { killed = append(killed, pid); return nil }
	reapWaitExit = func(pids []int, d time.Duration) []int { return nil } // all exit during grace

	reapOrphans(runDir, ownInstance)

	if len(killed) != 1 || killed[0] != orphanPid {
		t.Errorf("killed = %v, want a single SIGTERM to %d", killed, orphanPid)
	}
	if recordExists(runDir, orphanName) {
		t.Error("orphan record survived; a reaped orphan must be forgotten")
	}
	if recordExists(runDir, goneName) {
		t.Error("gone record survived; it must be forgotten")
	}
	if recordExists(runDir, foreignStaleName) {
		t.Error("foreign stale record survived; past the TTL it must be forgotten")
	}
	if !recordExists(runDir, mineName) {
		t.Error("our own child's record was removed; it must be kept")
	}
	if !recordExists(runDir, liveOwnerName) {
		t.Error("a live-owner child's record was removed; it must be kept")
	}
	if !recordExists(runDir, foreignFreshName) {
		t.Error("a foreign fresh record was removed; within the TTL it must be kept")
	}
	if recordExists(runDir, skipName) {
		t.Error("a verify-skip orphan's record survived; it must be forgotten")
	}
	if recordExists(runDir, earlierBootName) {
		t.Error("an earlier-boot record of this machine survived; it must be forgotten")
	}
	if !recordExists(runDir, passthroughName) {
		t.Error("a passthrough (disowning) record was removed; it must be kept")
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
// SIGTERM is sent SIGKILL, and one that outlives SIGKILL is counted survived. Both records
// are forgotten.
func TestReapTargetsTwoPhase(t *testing.T) {
	runDir := t.TempDir()
	dir := filepath.Join(runDir, "children")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t1 := reapTarget{pid: 1001, start: "10", name: "1001.json"}
	t2 := reapTarget{pid: 1002, start: "20", name: "1002.json"}
	for _, tt := range []reapTarget{t1, t2} {
		if err := os.WriteFile(filepath.Join(dir, tt.name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fakeLiveProcs(t, map[int]liveProc{
		t1.pid: {state: procAlive, startTicks: "10", pgid: t1.pid},
		t2.pid: {state: procAlive, startTicks: "20", pgid: t2.pid},
	})
	var sigs []struct {
		pid int
		sig syscall.Signal
	}
	oldKill, oldWait := killGroup, reapWaitExit
	t.Cleanup(func() { killGroup, reapWaitExit = oldKill, oldWait })
	killGroup = func(pid int, sig syscall.Signal) error {
		sigs = append(sigs, struct {
			pid int
			sig syscall.Signal
		}{pid, sig})
		return nil
	}
	// Grace: both survive. Escalate: t1 dies, t2 outlives SIGKILL.
	call := 0
	reapWaitExit = func(pids []int, d time.Duration) []int {
		call++
		if call == 1 {
			return pids // grace: none exited
		}
		return []int{t2.pid} // escalate: t2 still alive
	}
	var counts reapCounts
	reapTargets(runDir, []reapTarget{t1, t2}, &counts)

	// Both got SIGTERM, both got SIGKILL.
	term, kill := 0, 0
	for _, s := range sigs {
		switch s.sig {
		case syscall.SIGTERM:
			term++
		case syscall.SIGKILL:
			kill++
		}
	}
	if term != 2 || kill != 2 {
		t.Errorf("signals: %d SIGTERM, %d SIGKILL; want 2 and 2", term, kill)
	}
	if counts.reaped != 1 || counts.survived != 1 {
		t.Errorf("counts reaped=%d survived=%d, want 1 and 1", counts.reaped, counts.survived)
	}
	if recordExists(runDir, t1.name) || recordExists(runDir, t2.name) {
		t.Error("a handled target's record survived; both must be forgotten")
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
	fakeLiveProcs(t, map[int]liveProc{
		reused.pid:  {state: procAlive, startTicks: "999", pgid: reused.pid}, // start changed => reused
		gone.pid:    {state: procGone},
		sigfail.pid: {state: procAlive, startTicks: "30", pgid: sigfail.pid},
	})
	var killed []int
	oldKill, oldWait := killGroup, reapWaitExit
	t.Cleanup(func() { killGroup, reapWaitExit = oldKill, oldWait })
	killGroup = func(pid int, sig syscall.Signal) error {
		killed = append(killed, pid)
		if pid == sigfail.pid {
			return syscall.ESRCH // signal fails
		}
		return nil
	}
	reapWaitExit = func(pids []int, d time.Duration) []int { return nil }
	var counts reapCounts
	reapTargets(runDir, []reapTarget{reused, gone, sigfail}, &counts)

	// The reused pid is never signaled; gone is never signaled; sigfail is signaled once
	// (SIGTERM) and its error stops it there.
	for _, pid := range killed {
		if pid == reused.pid || pid == gone.pid {
			t.Errorf("signaled pid %d that should have been skipped by the re-check", pid)
		}
	}
	if recordExists(runDir, reused.name) || recordExists(runDir, gone.name) || recordExists(runDir, sigfail.name) {
		t.Error("a handled target's record survived; all must be forgotten")
	}
}

// TestReapOrphansBatchOverflow proves the sweep signals at most maxReapTargets orphans and
// leaves the rest for the next startup.
func TestReapOrphansBatchOverflow(t *testing.T) {
	oldMax := maxReapTargets
	t.Cleanup(func() { maxReapTargets = oldMax })
	maxReapTargets = 1

	runDir := t.TempDir()
	ownNode := nodeID()
	if ownNode == "" {
		t.Skip("nodeID unavailable on this host")
	}
	ownHost := "machine-id:" + machineID()
	table := map[int]liveProc{}
	for _, pid := range []int{700001, 700002} {
		writeRec(t, runDir, childRecord{
			Pid: pid, Node: ownNode, Host: ownHost, Instance: "inst-old",
			DaemonPid: 999999, DaemonStart: "1", Argv0: "/n", Start: "9", At: time.Now().UnixMilli(),
		})
		table[pid] = liveProc{state: procAlive, startTicks: "9", pgid: pid, program: "/n", runDir: runDir, childMark: strconv.Itoa(pid) + ":9"}
	}
	fakeLiveProcs(t, table)
	var killed int
	oldKill, oldWait := killGroup, reapWaitExit
	t.Cleanup(func() { killGroup, reapWaitExit = oldKill, oldWait })
	killGroup = func(int, syscall.Signal) error { killed++; return nil }
	reapWaitExit = func(pids []int, d time.Duration) []int { return nil }

	reapOrphans(runDir, "inst-self")
	if killed != 1 {
		t.Errorf("killed %d orphans, want exactly maxReapTargets=1", killed)
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

// TestRealReadLiveProc reads this live process through the real reader and confirms the
// alive state, the group id and the start-ticks, then confirms a missing pid reads gone.
func TestRealReadLiveProc(t *testing.T) {
	self := os.Getpid()
	lp := realReadLiveProc(self, true)
	if lp.state != procAlive {
		t.Fatalf("state = %d, want procAlive for self", lp.state)
	}
	if want := strconv.FormatInt(procStartTicks(self), 10); lp.startTicks != want {
		t.Errorf("startTicks = %q, want %q", lp.startTicks, want)
	}
	if want, _ := syscall.Getpgid(self); lp.pgid != want {
		t.Errorf("pgid = %d, want %d", lp.pgid, want)
	}
	if lp.program == "" {
		t.Error("program is empty; want this test binary's argv[0]")
	}
	if gone := realReadLiveProc(1<<30, false); gone.state != procGone {
		t.Errorf("state = %d for a missing pid, want procGone", gone.state)
	}
}

// TestRealReadLiveProcFakeProcfs exercises the parse-error arms against a fake /proc tree:
// an unparseable stat, a zombie, an empty cmdline, and a full record with both markers.
func TestRealReadLiveProcFakeProcfs(t *testing.T) {
	root := t.TempDir()
	oldRoot := procRoot
	t.Cleanup(func() { procRoot = oldRoot })
	procRoot = root

	mk := func(pid int, stat, cmdline, environ string) {
		d := filepath.Join(root, strconv.Itoa(pid))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "stat"), []byte(stat), 0o644); err != nil {
			t.Fatal(err)
		}
		if cmdline != "-" {
			if err := os.WriteFile(filepath.Join(d, "cmdline"), []byte(cmdline), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if environ != "-" {
			if err := os.WriteFile(filepath.Join(d, "environ"), []byte(environ), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	// No ')' in stat -> unparseable -> not ours.
	mk(10, "10 broken stat line", "x\x00", "")
	if lp := realReadLiveProc(10, true); lp.state != procNotOurs {
		t.Errorf("unparseable stat: state = %d, want procNotOurs", lp.state)
	}
	// Zombie state -> gone.
	mk(11, "11 (proc) Z "+fields19(), "x\x00", "")
	if lp := realReadLiveProc(11, true); lp.state != procGone {
		t.Errorf("zombie: state = %d, want procGone", lp.state)
	}
	// Empty cmdline with wantEnv -> gone.
	mk(12, "12 (proc) R "+fields19(), "", "")
	if lp := realReadLiveProc(12, true); lp.state != procGone {
		t.Errorf("empty cmdline: state = %d, want procGone", lp.state)
	}
	// Full record: markers parsed from environ.
	mk(13, "13 (proc) R "+fields19(), "/usr/bin/node\x00--flag\x00",
		"PATH=/bin\x00CLAUDE_SSH_RUN_DIR=/run/x\x00CLAUDE_SSH_CHILD=13:4242\x00")
	lp := realReadLiveProc(13, true)
	if lp.state != procAlive || lp.program != "/usr/bin/node" || lp.runDir != "/run/x" || lp.childMark != "13:4242" {
		t.Errorf("full record parsed wrong: %+v", lp)
	}
	if lp.pgid != 13 {
		t.Errorf("pgid = %d, want 13 (stat field 5)", lp.pgid)
	}
	// Unreadable cmdline (absent file) with wantEnv -> not ours.
	mk(14, "14 (proc) R "+fields19(), "-", "-")
	if lp := realReadLiveProc(14, true); lp.state != procNotOurs {
		t.Errorf("missing cmdline: state = %d, want procNotOurs", lp.state)
	}
	// A cmdline with no trailing NUL: the whole content is argv[0].
	mk(15, "15 (proc) R "+fields19(), "/bin/x", "")
	if lp := realReadLiveProc(15, true); lp.program != "/bin/x" {
		t.Errorf("no-NUL cmdline: program = %q, want /bin/x", lp.program)
	}
	// A readable cmdline but an unreadable environ -> not ours.
	mk(16, "16 (proc) R "+fields19(), "/bin/x\x00", "-")
	if lp := realReadLiveProc(16, true); lp.state != procNotOurs {
		t.Errorf("missing environ: state = %d, want procNotOurs", lp.state)
	}
}

// TestRealReadLiveProcOtherNamespace covers the pid-namespace guard on its own fake procfs.
// It is a separate test because it creates a self/ns/pid symlink, which would change how
// every other case in the shared fake tree resolves the guard.
func TestRealReadLiveProcOtherNamespace(t *testing.T) {
	root := t.TempDir()
	oldRoot := procRoot
	t.Cleanup(func() { procRoot = oldRoot })
	procRoot = root

	d := filepath.Join(root, "18")
	if err := os.MkdirAll(filepath.Join(d, "ns"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "stat"), []byte("18 (proc) R "+fields19()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "self", "ns"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The guard fires only when both ns/pid links resolve and differ.
	if err := os.Symlink("pid:[4026531836]", filepath.Join(root, "self", "ns", "pid")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("pid:[4026999999]", filepath.Join(d, "ns", "pid")); err != nil {
		t.Fatal(err)
	}
	if lp := realReadLiveProc(18, true); lp.state != procNotOurs {
		t.Errorf("other pid namespace: state = %d, want procNotOurs", lp.state)
	}
}

// TestReadChildRecordMissing covers the unreadable-file arm of readChildRecord.
func TestReadChildRecordMissing(t *testing.T) {
	if _, ok := readChildRecord(t.TempDir(), "nope.json"); ok {
		t.Error("readChildRecord reported ok for a missing file")
	}
}

// fields19 returns stat fields 4..22 with pgrp (field 5) = 13 and starttime (field 22) =
// 4242, so a "<pid> (proc) <state> " prefix plus this yields a parseable line.
func fields19() string {
	// After the ')': state(3) already supplied by caller. We supply ppid(4) pgrp(5) ...
	// up to starttime(22). Index in the fields-after-')' slice: state=0, ppid=1, pgrp=2,
	// ... starttime=19. So we need indices 1..19 here (ppid..starttime).
	f := []string{"1", "13"} // ppid=1, pgrp=13
	for i := 3; i <= 18; i++ {
		f = append(f, "0")
	}
	f = append(f, "4242") // starttime at index 19
	out := ""
	for i, s := range f {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}

// TestRealReapWaitExit drives the poll loop with seamed time and /proc: a pid that goes
// gone drops out, and a pid that stays alive survives to the deadline.
func TestRealReapWaitExit(t *testing.T) {
	oldRead, oldSleep, oldNow := readLiveProc, reapSleep, reapNow
	t.Cleanup(func() { readLiveProc, reapSleep, reapNow = oldRead, oldSleep, oldNow })
	reapSleep = func(time.Duration) {}
	now := time.Unix(0, 0)
	reapNow = func() time.Time { now = now.Add(60 * time.Millisecond); return now }

	polls := 0
	readLiveProc = func(pid int, wantEnv bool) liveProc {
		if pid == 3001 {
			polls++
			if polls >= 2 {
				return liveProc{state: procGone} // exits on the second poll
			}
			return liveProc{state: procAlive}
		}
		return liveProc{state: procAlive} // 3002 never exits
	}
	survivors := realReapWaitExit([]int{3001, 3002}, reapGrace)
	if len(survivors) != 1 || survivors[0] != 3002 {
		t.Errorf("survivors = %v, want [3002]", survivors)
	}
}
