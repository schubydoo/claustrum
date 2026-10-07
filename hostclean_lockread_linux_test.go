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

// TestPassLeavesADaemonWithUnreadOpenFilesAlone pins the first line of rows D1i and D1k
// (macOS VM, 89cb6289): a daemon of ours whose open files cannot be read gets the "left
// alone this pass" line before any run dir line, it counts as one undecided candidate, and
// no signal goes out. The answer of the read is staged through hcDaemonFilesUnread, so the
// shared part of the rule runs on linux. Every signal goes to a seam that only records.
// The control is the same host with open files that read: no line and no count.
func TestPassLeavesADaemonWithUnreadOpenFilesAlone(t *testing.T) {
	for _, unread := range []bool{true, false} {
		f := hcNewPassFixture(t)
		oldUnread := hcDaemonFilesUnread
		t.Cleanup(func() { hcDaemonFilesUnread = oldUnread })
		var asked []int
		hcDaemonFilesUnread = func(pid int) bool { asked = append(asked, pid); return unread }

		exe := filepath.Join(f.base, "srv", "a", "server")
		sibling := os.Getpid() + 1 // a daemon of ours on another socket
		hcFakeDaemonUID(t, f.proot, f.mk, f.link, sibling, exe,
			[]string{exe, "--serve", "--socket", filepath.Join(f.runRoot, "s1", rpcSockBasename)}, true, "1", f.uid)
		bridge := os.Getpid() + 2 // the daemon binary, but no serve process: never asked about
		hcFakeDaemonUID(t, f.proot, f.mk, f.link, bridge, exe, []string{exe, "--bridge"}, true, "1", f.uid)
		// The cleaner itself: a daemon of ours that the pass never asks about.
		hcFakeDaemonUID(t, f.proot, f.mk, f.link, os.Getpid(), exe,
			[]string{exe, "--serve", "--socket", f.c.ownSocket}, true, "1", f.uid)

		z0 := filepath.Join(f.runRoot, "z0") // a dead run dir, so the pass prints a run dir line
		if err := os.Mkdir(z0, 0o755); err != nil {
			t.Fatal(err)
		}
		hcBackdate(t, z0, 40*24*time.Hour)
		buf := captureLogBuf(t)

		sum := f.c.Pass()

		log := buf.String()
		line := fmt.Sprintf("[hostclean] daemon pid %d left alone this pass: its open files could not be inspected\n", sibling)
		removed := fmt.Sprintf("[hostclean] removed run dir %q:", z0)
		if !strings.Contains(log, removed) {
			t.Fatalf("unread=%v: the pass did not reach the tidy; log %q", unread, log)
		}
		if len(asked) != 1 || asked[0] != sibling {
			t.Errorf("unread=%v: asked about pids %v, want only the sibling %d", unread, asked, sibling)
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
		if strings.Count(log, "left alone this pass") != 1 || !strings.Contains(log, line) {
			t.Errorf("log = %q, want one line %q", log, line)
		}
		if strings.Index(log, line) > strings.Index(log, removed) {
			t.Errorf("the line comes after a run dir line; log %q", log)
		}
		if sum.undecided != 1 || sum.daemonsRetired != 0 || sum.runDirsRemoved != 1 {
			t.Errorf("summary = %+v, want undecided 1, retired 0, removed 1", sum)
		}
	}
}
