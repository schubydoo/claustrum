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
		{"empty node id", host, "", host, "bootB/ns1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := earlierBootOfThisMachine(tc.recHost, tc.recNode, tc.ownHost, tc.ownNode); got != tc.want {
				t.Errorf("earlierBootOfThisMachine = %v, want %v", got, tc.want)
			}
		})
	}
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

	sim := newProcSim(t)
	sim.live[orphanPid] = &liveProc{state: procAlive, startTicks: "9000", pgid: orphanPid, program: "/usr/bin/node", runDir: runDir, childMark: strconv.Itoa(orphanPid) + ":9000"}
	sim.live[333] = &liveProc{state: procAlive, startTicks: "50"}                                        // the live owning daemon
	sim.live[777] = &liveProc{state: procAlive, startTicks: "7", pgid: 5, program: "/n", runDir: runDir} // not group leader -> skip
	sim.live[888] = &liveProc{state: procNotOurs}                                                        // disowns us -> passthrough
	// deadDaemon and pid 444 are unlisted, so they read as gone.
	sim.diesOn(orphanPid, syscall.SIGTERM) // the reaped orphan exits to SIGTERM

	summary := captureLog(t, func() { reapOrphans(runDir, ownInstance) })

	if len(sim.kills) != 1 || sim.kills[0].pid != orphanPid || sim.kills[0].sig != syscall.SIGTERM {
		t.Errorf("kills = %v, want a single SIGTERM to %d", sim.kills, orphanPid)
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
	// Pin the lines of the sweep with the texts of 89cb6289: the record line of the
	// skip, the ending line of the group, and the six numbers of the summary. One group
	// ended on SIGTERM, four records dropped, four kept. A mutant that reorders or
	// mis-labels the numbers fails here.
	wantLinesInOrder(t, summary,
		"INFO  [process.Registry] record 777.json: pid 777 is not a child to end (it does not lead its own process group); dropping the record, signalling nothing",
		`INFO  [process.Registry] ending orphaned process group 424242 recorded by daemon instance "inst-old" (pid 999999): SIGTERM`,
		"INFO  [daemon] serve: predecessor's children — 1 orphaned group(s): 1 ended on SIGTERM, 0 on SIGKILL, 0 survived; 4 stale record(s) dropped, 4 kept",
	)
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
	sim := newProcSim(t)
	for pid, lp := range table {
		sim.live[pid] = &lp
		sim.diesOn(pid, syscall.SIGTERM)
	}

	out := captureLog(t, func() { reapOrphans(runDir, "inst-self") })
	if sim.sigCount(syscall.SIGTERM) != 1 {
		t.Errorf("signaled %d orphans, want exactly maxReapTargets=1", sim.sigCount(syscall.SIGTERM))
	}
	// The line for the groups over the limit comes before the first ending line, and
	// the summary counts the group that stays as kept.
	wantLinesInOrder(t, out,
		"WARN  [process.Registry] 1 more orphaned group(s) recorded in "+runDir+"/children than the 1 one start ends; leaving those for the next start",
		"INFO  [daemon] serve: predecessor's children — 1 orphaned group(s): 1 ended on SIGTERM, 0 on SIGKILL, 0 survived; 0 stale record(s) dropped, 1 kept",
	)
	if names := childrenNames(t, filepath.Join(runDir, "children")); len(names) != 1 {
		t.Errorf("records left: %v, want the one of the group over the limit", names)
	}
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
	if len(lp.args) != 1 || lp.args[0] != "--flag" {
		t.Errorf("args = %q, want the one argument after argv[0]", lp.args)
	}
	// An argument with a space stays one argument: the reap matches a program key
	// against whole arguments.
	mk(17, "17 (proc) R "+fields19(), "/bin/sh\x00/f/bin/my target\x00-x\x00", "")
	if lp := realReadLiveProc(17, true); len(lp.args) != 2 || lp.args[0] != "/f/bin/my target" || lp.args[1] != "-x" {
		t.Errorf("args = %q, want the two whole arguments", lp.args)
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
	lp16 := realReadLiveProc(16, true)
	if lp16.state != procNotOurs {
		t.Errorf("missing environ: state = %d, want procNotOurs", lp16.state)
	}
	// The read says why, in the reference's words, for the no-longer-verifies line.
	if want := "environment unreadable: open " + filepath.Join(root, "16", "environ") + ": no such file or directory"; lp16.unreadable != want {
		t.Errorf("missing environ: unreadable = %q, want %q", lp16.unreadable, want)
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

// fields19 returns stat fields 4..23 with pgrp (field 5) = 13, starttime (field 22) =
// 4242 and a non-zero vsize (field 23), so a "<pid> (proc) <state> " prefix plus this
// yields a parseable line.
//
// The vsize is why the name no longer matches the count. A vsize of
// 0 counts as gone as well as the Z and X state letters, so a line that stops at
// starttime cannot answer the question and reads as not ours.
func fields19() string {
	// After the ')': state(3) already supplied by caller. We supply ppid(4) pgrp(5) ...
	// up to vsize(23). Index in the fields-after-')' slice: state=0, ppid=1, pgrp=2,
	// ... starttime=19, vsize=20. So we need indices 1..20 here.
	f := []string{"1", "13"} // ppid=1, pgrp=13
	for i := 3; i <= 18; i++ {
		f = append(f, "0")
	}
	f = append(f, "4242")    // starttime at index 19
	f = append(f, "4194304") // vsize at index 20, non-zero so the process reads alive
	out := ""
	for i, s := range f {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}
