//go:build linux || darwin

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// recordDirOrder returns the .json names of dir in directory order, the order of `ls -U`. It
// reads the folder by itself, not through the code under test.
func recordDirOrder(t *testing.T, dir string) []string {
	t.Helper()
	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		t.Fatal(err)
	}
	return slices.DeleteFunc(names, func(n string) bool { return !strings.HasSuffix(n, ".json") })
}

// pidOfName is the pid of a <pid>.json record name.
func pidOfName(t *testing.T, name string) int {
	t.Helper()
	pid, err := strconv.Atoi(strings.TrimSuffix(name, ".json"))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// plantOrphanRecords writes n records of a dead daemon into runDir, with pids from
// first up, and returns their names in directory order. It skips the test when that
// order is the name order, because the test then cannot tell the two apart.
func plantOrphanRecords(t *testing.T, runDir string, first, n int) []string {
	t.Helper()
	for i := range n {
		writeRec(t, runDir, orphanRecord(first+i, "/f/bin/sigstub", ""))
	}
	order := recordDirOrder(t, filepath.Join(runDir, "children"))
	if len(order) != n {
		t.Fatalf("%d record names in the folder, want %d", len(order), n)
	}
	if slices.IsSorted(order) {
		t.Skip("this file system lists the folder in name order, so the walk order cannot be told from it")
	}
	return order
}

// liveOrphan makes pid a live process that passes the whole record test for runDir and
// that ends on sig.
func liveOrphan(sim *procSim, pid int, runDir string, sig syscall.Signal) {
	sim.live[pid] = &liveProc{state: procAlive, startTicks: "9000", pgid: pid,
		program: "/f/bin/sigstub", runDir: runDir, childMark: strconv.Itoa(pid) + ":9000"}
	sim.diesOn(pid, sig)
}

// TestReapHandles128NamesInDirectoryOrder pins row C1 (89cb6289, Linux): of 131 record
// names, one start handles the first 128 of the directory order and prints one line
// for the rest. The live child among them gets SIGTERM, and the 127 others count as
// dropped. A second start removes the last 3 and prints no line.
func TestReapHandles128NamesInDirectoryOrder(t *testing.T) {
	if node, _ := ownReapIdentity(); node == "" {
		t.Skip("no node identity on this host")
	}
	runDir := t.TempDir()
	children := filepath.Join(runDir, "children")
	order := plantOrphanRecords(t, runDir, 800001000, 131)
	sim := newProcSim(t)
	live := pidOfName(t, order[97]) // row C1: the live record at place 97
	liveOrphan(sim, live, runDir, syscall.SIGTERM)

	out := captureLog(t, func() { reapOrphans(runDir, "inst-self") })

	left := recordDirOrder(t, children)
	slices.Sort(left)
	want := slices.Clone(order[128:])
	slices.Sort(want)
	if !slices.Equal(left, want) {
		t.Errorf("records left after the first start: %v\nwant the last 3 of the directory order: %v", left, want)
	}
	if sim.sigCount(syscall.SIGTERM) != 1 || !sim.signaled(live) {
		t.Errorf("kills = %v, want one SIGTERM to the live group %d", sim.kills, live)
	}
	wantLinesInOrder(t, out,
		"WARN  [process.Registry] more than 128 records in "+children+"; leaving the rest for the next start",
		`INFO  [process.Registry] ending orphaned process group `+strconv.Itoa(live)+` recorded by daemon instance "inst-old" (pid 999999999): SIGTERM`,
		"INFO  [daemon] serve: predecessor's children — 1 orphaned group(s): 1 ended on SIGTERM, 0 on SIGKILL, 0 survived; 127 stale record(s) dropped, 0 kept",
	)

	out = captureLog(t, func() { reapOrphans(runDir, "inst-self") })
	if left := recordDirOrder(t, children); len(left) != 0 {
		t.Errorf("records left after the second start: %v, want none", left)
	}
	if strings.Contains(out, "[process.Registry]") || strings.Contains(out, "predecessor's children") {
		t.Errorf("the second start logged a line for removed records only:\n%s", out)
	}
}

// TestReapEnds64GroupsInDirectoryOrder pins rows C2, C4 and MR03c (89cb6289, Linux):
// with 129 recorded live children, one start reads 128 records, sends SIGTERM to the
// first 64 of the directory order, in that order, and prints the two limit lines first.
// The summary counts the 64 verified groups that it left as kept.
func TestReapEnds64GroupsInDirectoryOrder(t *testing.T) {
	if node, _ := ownReapIdentity(); node == "" {
		t.Skip("no node identity on this host")
	}
	runDir := t.TempDir()
	children := filepath.Join(runDir, "children")
	order := plantOrphanRecords(t, runDir, 800002000, 129)
	sim := newProcSim(t)
	for _, name := range order {
		liveOrphan(sim, pidOfName(t, name), runDir, syscall.SIGTERM)
	}

	out := captureLog(t, func() { reapOrphans(runDir, "inst-self") })

	var termed []string
	for _, k := range sim.kills {
		if k.sig == syscall.SIGTERM {
			termed = append(termed, strconv.Itoa(k.pid)+".json")
		}
	}
	if !slices.Equal(termed, order[:64]) {
		t.Errorf("SIGTERM order:\n%v\nwant the first 64 names of the directory order:\n%v", termed, order[:64])
	}
	if left := recordDirOrder(t, children); len(left) != 65 {
		t.Errorf("%d records left, want 65", len(left))
	}
	wantLinesInOrder(t, out,
		"WARN  [process.Registry] more than 128 records in "+children+"; leaving the rest for the next start",
		"WARN  [process.Registry] 64 more orphaned group(s) recorded in "+children+" than the 64 one start ends; leaving those for the next start",
		`INFO  [process.Registry] ending orphaned process group `+strings.TrimSuffix(order[0], ".json")+` recorded by daemon instance "inst-old" (pid 999999999): SIGTERM`,
		"INFO  [daemon] serve: predecessor's children — 64 orphaned group(s): 64 ended on SIGTERM, 0 on SIGKILL, 0 survived; 0 stale record(s) dropped, 64 kept",
	)
}

// TestReapReadsAtMost4096BytesOfARecord pins rows C3a, C3b and ED14g (89cb6289, Linux).
// A record of more than 5000 bytes is removed with no signal and no line, also when
// its program key names the live program. A valid record with blanks after it, 2 MiB
// in all, is read and its child gets SIGTERM.
func TestReapReadsAtMost4096BytesOfARecord(t *testing.T) {
	if node, _ := ownReapIdentity(); node == "" {
		t.Skip("no node identity on this host")
	}
	runDir := t.TempDir()
	children := filepath.Join(runDir, "children")
	const long, longProgram, padded = 800003001, 800003002, 800003003
	// C3a: an argv0 of 5000 characters. C3b: the same with a program key that names
	// the live program, so only the size can stop the signal.
	writeRec(t, runDir, orphanRecord(long, "/"+strings.Repeat("a", 4999), ""))
	writeRec(t, runDir, orphanRecord(longProgram, "/"+strings.Repeat("a", 4999), "/f/bin/sigstub"))
	// ED14g: a valid record, then blanks up to 2 MiB.
	name := writeRec(t, runDir, orphanRecord(padded, "/f/bin/sigstub", ""))
	f, err := os.OpenFile(filepath.Join(children, name), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := f.Stat()
	if _, err := f.WriteString(strings.Repeat(" ", 2<<20-int(fi.Size()))); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	sim := newProcSim(t)
	for _, pid := range []int{long, longProgram, padded} {
		liveOrphan(sim, pid, runDir, syscall.SIGTERM)
	}

	out := captureLog(t, func() { reapOrphans(runDir, "inst-self") })

	if sim.signaled(long) || sim.signaled(longProgram) {
		t.Errorf("a child with a record over the size limit got a signal: %v", sim.kills)
	}
	if sim.sigCount(syscall.SIGTERM) != 1 || !sim.signaled(padded) {
		t.Errorf("kills = %v, want one SIGTERM to the child %d of the padded record", sim.kills, padded)
	}
	if left := recordDirOrder(t, children); len(left) != 0 {
		t.Errorf("records left: %v, want none", left)
	}
	if strings.Contains(out, "is not a child to end") {
		t.Errorf("the sweep logged a line for a record over the size limit:\n%s", out)
	}
}

// TestReapRecordBoundIs4096Bytes pins row P8 (89cb6289, Linux): a record padded in an
// added last field to 4096 bytes is read and its child gets SIGTERM. A record of 4097
// bytes gets no signal and is removed.
func TestReapRecordBoundIs4096Bytes(t *testing.T) {
	if node, _ := ownReapIdentity(); node == "" {
		t.Skip("no node identity on this host")
	}
	runDir := t.TempDir()
	children := filepath.Join(runDir, "children")
	const atBound, overBound = 800003101, 800003102
	for pid, size := range map[int]int{atBound: 4096, overBound: 4097} {
		name := writeRec(t, runDir, orphanRecord(pid, "/f/bin/sigstub", ""))
		data, err := json.Marshal(orphanRecord(pid, "/f/bin/sigstub", ""))
		if err != nil {
			t.Fatal(err)
		}
		head := string(data[:len(data)-1]) + `,"pad":"`
		padded := head + strings.Repeat("x", size-len(head)-2) + `"}`
		if len(padded) != size {
			t.Fatalf("record of %d bytes, want %d", len(padded), size)
		}
		if err := os.WriteFile(filepath.Join(children, name), []byte(padded), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sim := newProcSim(t)
	liveOrphan(sim, atBound, runDir, syscall.SIGTERM)
	liveOrphan(sim, overBound, runDir, syscall.SIGTERM)

	reapOrphans(runDir, "inst-self")

	if sim.sigCount(syscall.SIGTERM) != 1 || !sim.signaled(atBound) || sim.signaled(overBound) {
		t.Errorf("kills = %v, want one SIGTERM to the child %d of the 4096-byte record", sim.kills, atBound)
	}
	if left := recordDirOrder(t, children); len(left) != 0 {
		t.Errorf("records left: %v, want none", left)
	}
}

// TestReapRemovesRecordsOfSignalledGroupsTogether pins row RP04 (89cb6289, Linux): the
// record of a group that ends on its SIGTERM stays while another group still waits for
// its SIGKILL. Both records go when the last group is done.
func TestReapRemovesRecordsOfSignalledGroupsTogether(t *testing.T) {
	if node, _ := ownReapIdentity(); node == "" {
		t.Skip("no node identity on this host")
	}
	runDir := t.TempDir()
	const quick, slow = 800004001, 800004002
	quickName := writeRec(t, runDir, orphanRecord(quick, "/f/bin/sigstub", ""))
	slowName := writeRec(t, runDir, orphanRecord(slow, "/f/bin/sigstub", ""))
	sim := newProcSim(t)
	liveOrphan(sim, quick, runDir, syscall.SIGTERM)
	liveOrphan(sim, slow, runDir, syscall.SIGKILL)

	// At each poll of the wait the quick group is gone already. Its record must still
	// be there until the slow group got its SIGKILL.
	simSleep := reapSleep
	polls, quickGoneEarly := 0, false
	reapSleep = func(d time.Duration) {
		if d == reapPollInterval && sim.sigCount(syscall.SIGKILL) == 0 {
			polls++
			if !recordExists(runDir, quickName) {
				quickGoneEarly = true
			}
		}
		simSleep(d)
	}

	reapOrphans(runDir, "inst-self")

	if polls == 0 {
		t.Fatal("the sweep did not wait for the slow group")
	}
	if quickGoneEarly {
		t.Error("the record of the group that ended on SIGTERM went before the last group was done")
	}
	if recordExists(runDir, quickName) || recordExists(runDir, slowName) {
		t.Error("a record of a signalled group is still there after the sweep")
	}
}

// TestReapLines pins the lines of one sweep with the texts of 89cb6289 (Linux and macOS
// rows A4 and D1, and row RP07 for the order): the record line of a dropped record
// first, the ending line of each group at its SIGTERM, the outlived line at the
// SIGKILL, and the summary with its six numbers.
func TestReapLines(t *testing.T) {
	node, host := ownReapIdentity()
	if node == "" {
		t.Skip("no node identity on this host")
	}
	runDir := t.TempDir()
	const onTerm, onKill, dropped, kept, owner = 800005001, 800005002, 800005003, 800005004, 800005005
	writeRec(t, runDir, orphanRecord(onTerm, "/f/bin/sigstub", ""))
	writeRec(t, runDir, orphanRecord(onKill, "/f/bin/sigstub", ""))
	writeRec(t, runDir, orphanRecord(dropped, "/nonexistent/other", ""))
	// A record whose owning daemon is alive stays.
	writeRec(t, runDir, childRecord{Pid: kept, Node: node, Host: host, Instance: "inst-live",
		DaemonPid: owner, DaemonStart: "50", Argv0: "/f/bin/sigstub", Start: "9000", At: time.Now().UnixMilli()})
	sim := newProcSim(t)
	liveOrphan(sim, onTerm, runDir, syscall.SIGTERM)
	liveOrphan(sim, onKill, runDir, syscall.SIGKILL)
	liveOrphan(sim, dropped, runDir, syscall.SIGTERM)
	sim.live[owner] = &liveProc{state: procAlive, startTicks: "50", pgid: owner}

	out := captureLog(t, func() { reapOrphans(runDir, "inst-self") })

	ending := func(pid int) string {
		return `INFO  [process.Registry] ending orphaned process group ` + strconv.Itoa(pid) + ` recorded by daemon instance "inst-old" (pid 999999999): SIGTERM`
	}
	const recordLine = `INFO  [process.Registry] record 800005003.json: pid 800005003 is not a child to end (it does not run the recorded program "/nonexistent/other"); dropping the record, signalling nothing`
	const outlived = "WARN  [process.Registry] group 800005002 outlived SIGTERM for 2s: SIGKILL"
	const summary = "INFO  [daemon] serve: predecessor's children — 2 orphaned group(s): 1 ended on SIGTERM, 1 on SIGKILL, 0 survived; 1 stale record(s) dropped, 1 kept"
	wantLinesInOrder(t, out, recordLine, ending(onTerm), outlived, summary)
	wantLinesInOrder(t, out, recordLine, ending(onKill), outlived, summary)
	// The capture is shared by the whole process, so only the lines of a sweep count.
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "[process.Registry]") || strings.Contains(line, "predecessor's children") {
			n++
		}
	}
	if n != 5 {
		t.Errorf("the sweep logged %d lines of its own, want 5:\n%s", n, out)
	}
	if sim.signaled(dropped) || sim.signaled(kept) {
		t.Errorf("a dropped or kept record got a signal: %v", sim.kills)
	}
}

// TestReapSummaryOfASweepThatOnlyKeeps pins the summary of a sweep that sent no signal
// and kept one record, the record of a daemon that is alive. The rule is the one that
// reapSummary states: a kept record prints the summary.
func TestReapSummaryOfASweepThatOnlyKeeps(t *testing.T) {
	node, host := ownReapIdentity()
	if node == "" {
		t.Skip("no node identity on this host")
	}
	runDir := t.TempDir()
	const kept, owner = 800005101, 800005102
	name := writeRec(t, runDir, childRecord{Pid: kept, Node: node, Host: host, Instance: "inst-live",
		DaemonPid: owner, DaemonStart: "50", Argv0: "/f/bin/sigstub", Start: "9000", At: time.Now().UnixMilli()})
	sim := newProcSim(t)
	sim.live[owner] = &liveProc{state: procAlive, startTicks: "50", pgid: owner}

	out := captureLog(t, func() { reapOrphans(runDir, "inst-self") })

	const summary = "INFO  [daemon] serve: predecessor's children — 0 orphaned group(s): 0 ended on SIGTERM, 0 on SIGKILL, 0 survived; 0 stale record(s) dropped, 1 kept\n"
	if !strings.Contains(out, summary) {
		t.Errorf("log = %q\nwant the summary %q", out, summary)
	}
	if len(sim.kills) != 0 || !recordExists(runDir, name) {
		t.Errorf("kills = %v, record there = %v, want no signal and the record", sim.kills, recordExists(runDir, name))
	}
}

// TestReapRecordLineReasons pins the record line of the walk for each reason text of
// 89cb6289 (Linux rows RP07, ED10a to ED10d, ED01a, ED04b, ED02a and ED07). A sweep
// that only drops records prints no summary (row ED01a).
func TestReapRecordLineReasons(t *testing.T) {
	if node, _ := ownReapIdentity(); node == "" {
		t.Skip("no node identity on this host")
	}
	const pid = 800006001
	mark := strconv.Itoa(pid) + ":9000"
	for _, tc := range []struct {
		name   string
		rec    func(*childRecord)
		live   func(lp *liveProc, runDir string)
		reason string
	}{
		{"RP07 not the group leader", nil, func(lp *liveProc, _ string) { lp.pgid = 7 },
			"it does not lead its own process group"},
		{"ED10b another run dir", nil, func(lp *liveProc, _ string) { lp.runDir = "/run/c2" },
			"it belongs to a daemon of another run dir: its CLAUDE_SSH_RUN_DIR names a different one"},
		{"ED10a no run dir entry", nil, func(lp *liveProc, _ string) { lp.runDir = "" },
			"it was not started by a daemon of this run dir: no CLAUDE_SSH_RUN_DIR names it"},
		{"ED10c the child mark names another pid", nil, func(lp *liveProc, _ string) { lp.childMark = "999:9000" },
			"it was not started by a daemon itself: its CLAUDE_SSH_CHILD does not name its own pid and start (something a child started)"},
		{"ED01a another program", func(r *childRecord) { r.Argv0 = "/nonexistent/other" }, nil,
			`it does not run the recorded program "/nonexistent/other"`},
		{"ED04b another program and a program key", func(r *childRecord) { r.Argv0, r.Program = "/nonexistent/other", "/tmp/e/bin/../bin/target" }, nil,
			`it does not run the recorded program "/nonexistent/other" and neither runs nor was handed "/tmp/e/bin/../bin/target"`},
		{"ED02a no program in the record", func(r *childRecord) { r.Argv0 = "" }, nil,
			"the record names no program to verify against"},
		{"ED07 another start", func(r *childRecord) { r.Start = "9001" }, nil,
			`it did not start at the recorded "9001": the pid has been reused`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runDir := t.TempDir()
			rec := orphanRecord(pid, "/f/bin/sigstub", "")
			if tc.rec != nil {
				tc.rec(&rec)
			}
			name := writeRec(t, runDir, rec)
			sim := newProcSim(t)
			lp := &liveProc{state: procAlive, startTicks: "9000", pgid: pid, program: "/f/bin/sigstub", runDir: runDir, childMark: mark}
			if tc.live != nil {
				tc.live(lp, runDir)
			}
			sim.live[pid] = lp

			out := captureLog(t, func() { reapOrphans(runDir, "inst-self") })

			want := "INFO  [process.Registry] record " + name + ": pid " + strconv.Itoa(pid) + " is not a child to end (" + tc.reason + "); dropping the record, signalling nothing\n"
			// The capture is shared by the whole process, so a line of another
			// test can be in it. Only the lines of a sweep count.
			if !strings.Contains(out, want) || strings.Count(out, "[process.Registry] record ") != 1 ||
				strings.Contains(out, "predecessor's children") {
				t.Errorf("log = %q\nwant one record line and no summary: %q", out, want)
			}
			if len(sim.kills) != 0 || recordExists(runDir, name) {
				t.Errorf("kills = %v, record there = %v, want no signal and no record", sim.kills, recordExists(runDir, name))
			}
		})
	}
}
