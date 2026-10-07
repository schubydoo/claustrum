//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hcSiblingDaemon plants a serve daemon of ours on <runRoot>/<name>/rpc.sock and makes that
// run dir, idle for the given time. startTicks sets its age: "1" is old.
func hcSiblingDaemon(t *testing.T, f *hcPassFix, pid int, name, startTicks string, idle time.Duration) {
	t.Helper()
	exe := filepath.Join(f.base, "srv", "a", "server")
	dir := filepath.Join(f.runRoot, name)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	hcBackdate(t, dir, idle)
	hcFakeDaemonUID(t, f.proot, f.mk, f.link, pid, exe,
		[]string{exe, "--serve", "--socket", filepath.Join(dir, rpcSockBasename)}, true, startTicks, f.uid)
}

// TestPassLeavesADaemonWithUnreadOpenFilesAlone pins the "left alone this pass" line of rows
// D1i, D1k, E1 and E2 (macOS VM, 89cb6289). A daemon of ours that is old and serves an idle
// run dir, and whose open files cannot be read, gets the line before any run dir line. It
// counts as one undecided candidate, and no signal goes out. Row E2: two such daemons get
// one line each. Row E1: a young daemon on a fresh run dir gets none. The answer of the
// read is staged through hcDaemonFilesUnread, so the shared part of the rule runs on linux.
// Every signal goes to a seam that only records. The control is the same host with open
// files that read: no line and no count.
func TestPassLeavesADaemonWithUnreadOpenFilesAlone(t *testing.T) {
	for _, unread := range []bool{true, false} {
		f := hcNewPassFixture(t)
		oldUnread := hcDaemonFilesUnread
		t.Cleanup(func() { hcDaemonFilesUnread = oldUnread })
		var asked []int
		hcDaemonFilesUnread = func(pid int) bool { asked = append(asked, pid); return unread }

		const old = 40 * 24 * time.Hour
		self := os.Getpid()
		hcSiblingDaemon(t, f, self+1, "s1", "1", old)
		hcSiblingDaemon(t, f, self+2, "s3", "1", old)
		// Uptime is 100000 s in the fake /proc, so start tick 9999000 is 10 s ago.
		hcSiblingDaemon(t, f, self+3, "s2", "9999000", time.Second) // young, fresh run dir
		hcSiblingDaemon(t, f, self+4, "s4", "9999000", old)         // young, idle run dir
		hcSiblingDaemon(t, f, self+5, "s5", "1", time.Second)       // old, fresh run dir
		exe := filepath.Join(f.base, "srv", "a", "server")
		// The daemon binary, but no serve process, and the cleaner itself: never asked about.
		hcFakeDaemonUID(t, f.proot, f.mk, f.link, self+6, exe, []string{exe, "--bridge"}, true, "1", f.uid)
		hcFakeDaemonUID(t, f.proot, f.mk, f.link, self, exe,
			[]string{exe, "--serve", "--socket", f.c.ownSocket}, true, "1", f.uid)
		buf := captureLogBuf(t)

		sum := f.c.Pass()

		log := buf.String()
		firstDir := strings.Index(log, "[hostclean] removed run dir ")
		if firstDir < 0 {
			t.Fatalf("unread=%v: the pass did not reach the tidy; log %q", unread, log)
		}
		if len(asked) != 2 || asked[0]+asked[1] != 2*self+3 {
			t.Errorf("unread=%v: asked about pids %v, want only the two old idle siblings %d and %d", unread, asked, self+1, self+2)
		}
		if len(f.single) != 0 || len(f.group) != 0 {
			t.Errorf("unread=%v: signals single=%v group=%v, want none", unread, f.single, f.group)
		}
		if !unread {
			if strings.Contains(log, "left alone this pass") || sum.undecided != 0 {
				t.Errorf("control: undecided=%d log=%q, want no line and no count", sum.undecided, log)
			}
			continue
		}
		for _, pid := range []int{self + 1, self + 2} {
			line := fmt.Sprintf("[hostclean] daemon pid %d left alone this pass: its open files could not be inspected\n", pid)
			if i := strings.Index(log, line); i < 0 || i > firstDir {
				t.Errorf("log = %q, want %q before the first run dir line", log, line)
			}
		}
		if strings.Count(log, "left alone this pass") != 2 || sum.undecided != 2 || sum.daemonsRetired != 0 {
			t.Errorf("summary = %+v log = %q, want two lines, undecided 2, retired 0", sum, log)
		}
	}
}

// TestPassOrphanLineWithoutAStdioAnswer pins row E5 (macOS VM, 89cb6289): an orphaned group
// whose stdio read got no answer gets the "left alone this pass" line of the reference, it
// counts as undecided, and no signal goes out. The control is a read that answered and
// could not read the descriptors: it keeps claustrum's own line. The read is staged through
// hcStdioRead, and every signal goes to a seam that only records.
func TestPassOrphanLineWithoutAStdioAnswer(t *testing.T) {
	for _, answered := range []bool{false, true} {
		f := hcNewPassFixture(t)
		oldRead := hcStdioRead
		t.Cleanup(func() { hcStdioRead = oldRead })
		hcStdioRead = func(int) (bool, bool, bool) { return false, false, answered }
		orphan := os.Getpid() + 1
		hcFakeDaemonUID(t, f.proot, f.mk, f.link, orphan, filepath.Join(f.base, "ccd-cli", "2.1.0"),
			[]string{"claude", "--output-format=stream-json"}, true, "1", f.uid)
		buf := captureLogBuf(t)

		sum := f.c.Pass()

		want := fmt.Sprintf("[hostclean] process group %d left alone this pass: its descriptors could not be inspected\n", orphan)
		if answered {
			want = fmt.Sprintf("[hostclean] spared process group %d this sweep: its open descriptors were unreadable\n", orphan)
		}
		log := buf.String()
		if !strings.Contains(log, want) || strings.Count(log, fmt.Sprintf("process group %d ", orphan)) != 1 {
			t.Errorf("answered=%v: log = %q, want one group line %q", answered, log, want)
		}
		if sum.undecided != 1 || sum.orphanSignalled != 0 || len(f.group) != 0 || len(f.single) != 0 {
			t.Errorf("answered=%v: summary=%+v group=%v single=%v, want undecided 1 and no signal", answered, sum, f.group, f.single)
		}
	}
}
