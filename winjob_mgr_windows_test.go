//go:build windows

package main

import (
	"strings"
	"testing"
	"time"
)

// TestKillEndsOnlyTheDirectChild: process.kill, process.killAndWait and the kill of a
// clean stop end the direct child, with exit code 1 and the "asked to end by this daemon (client)"
// exit line. That line is claustrum's own on Windows (not measured), and Linux measured it for build 52d82f0a.
// 89cb6289 printed ", signalled at client request" there. The grandchild is alive 1 s later. All rows run their
// kills first, and then one wait of 1 s serves the check of every grandchild. Measured on Windows against 89cb6289:
// only the direct child ends, and the grandchild is alive at 1 s, 5 s and 15 s. The rows are seven signal forms of
// process.kill, three forms of process.killAndWait, -stop and server.shutdown. claustrum gave the same states in
// those rows. The manager here has no job, the state of CLAUDE_SSH_NO_JOBS=1. TestJobEndsTheTree has the jobs.
func TestKillEndsOnlyTheDirectChild(t *testing.T) {
	type result struct{ found, died, already, escalated bool }
	// Each row has its own child id, so a child that another test or row left alive cannot
	// add a matching exit line. The row id is the child id.
	actions := []struct {
		id      string
		act     func(m *procManager, id string) result
		wantLog string
	}{
		{"kd-kill-default", func(m *procManager, id string) result { _ = m.kill(id, ""); return result{} },
			", asked to end by this daemon (client)"},
		{"kd-kill-term", func(m *procManager, id string) result { _ = m.kill(id, "TERM"); return result{} },
			", asked to end by this daemon (client)"},
		{"kd-kill-kill", func(m *procManager, id string) result { _ = m.kill(id, "KILL"); return result{} },
			", asked to end by this daemon (client)"},
		{"kd-killAndWait-default", func(m *procManager, id string) result {
			f, d, a, e := m.killAndWait(id, "", 3*time.Second, true)
			return result{f, d, a, e}
		}, ", asked to end by this daemon (client)"},
		{"kd-killAndWait-no-escalate", func(m *procManager, id string) result {
			f, d, a, e := m.killAndWait(id, "", 3*time.Second, false)
			return result{f, d, a, e}
		}, ", asked to end by this daemon (client)"},
		// The exit line of a clean stop is not measured on Windows. Only the code is, so
		// the empty wantLog checks the code alone.
		{"kd-clean-stop", func(m *procManager, _ string) result { m.killAll(); return result{} }, ""},
	}
	// The grandchild of each row, by its child id. The parent test ends each one at its
	// end, so a grandchild lives past its row until the check below.
	parent := t
	gpids := map[string]int{}
	for _, tc := range actions {
		t.Run(tc.id, func(t *testing.T) {
			m := newTestProcManager(t)
			p, gpid := spawnTreeFor(t, parent, m, tc.id, "tree")
			gpids[tc.id] = gpid
			buf := captureLogBuf(t)

			got := tc.act(m, tc.id)
			select {
			case <-p.done:
			case <-time.After(10 * time.Second):
				t.Fatal("the direct child did not end")
			}
			if strings.HasPrefix(tc.id, "kd-killAndWait") && got != (result{found: true, died: true}) {
				t.Errorf("killAndWait = %+v, want found and died only", got)
			}
			// A child left alive by another test can add its own exit line after this one,
			// so the check takes the exit lines of this row's id and not the end of the log.
			if lines := exitLinesOf(buf.String(), tc.id); len(lines) != 1 {
				t.Errorf("log = %q, want exactly one exit line of %s with code 1, got %d", buf.String(), tc.id, len(lines))
			} else if w := exitLineOf(tc.id) + tc.wantLog; tc.wantLog != "" && lines[0] != w {
				t.Errorf("exit line = %q, want %q", lines[0], w)
			}
			if strings.Contains(buf.String(), "KillAndWait "+tc.id+": group kill failed") {
				t.Errorf("log = %q, want no group kill line", buf.String())
			}
		})
	}
	// One window of 1 s for all rows. The last row ended its child last, so each
	// grandchild is checked at least 1 s after the end of its direct child. A poll
	// fails at once when a grandchild ends. A grandchild that the window left is
	// checked once at its end. A grandchild of the "tree" helper ends by itself
	// after 60 s, so all rows and this window must end well inside 60 s.
	deadline := time.Now().Add(time.Second)
	for _, tc := range actions {
		gpid, ok := gpids[tc.id]
		if !ok {
			continue // The row did not run, or it failed before its grandchild started and said so.
		}
		if waitProcessGone(gpid, time.Until(deadline)) || !processAlive(t, gpid) {
			t.Errorf("%s: grandchild %d ended with the direct child", tc.id, gpid)
		}
	}
}

// TestKillWithKidsHoldingThePipes: a grandchild holds the stdout and stderr of the child.
// A kill ends the child at once, and the exit of the managed process waits for the drain
// grace. Measured on Windows against 89cb6289: process.kill answers at once and its exit
// frame comes 5.002 s later. process.killAndWait answers after 5.005 s with found, died
// and escalated, and the log holds the lines below. The graces are shrunk here: the kill
// grace ends first, then the drain grace. The manager has no job. With
// CLAUDE_SSH_NO_JOBS=1, 52d82f0a logs the drain line too when the child exits by itself
// (measured).
func TestKillWithKidsHoldingThePipes(t *testing.T) {
	oldDrain := exitDrainGrace
	exitDrainGrace = 1500 * time.Millisecond
	t.Cleanup(func() { exitDrainGrace = oldDrain })

	t.Run("killAndWait", func(t *testing.T) {
		const id = "kp-killAndWait"
		m := newTestProcManager(t)
		_, gpid := spawnTree(t, m, id, "tree-stdio")
		buf := captureLogBuf(t)

		start := time.Now()
		found, died, already, escalated := m.killAndWait(id, "", 300*time.Millisecond, true)
		took := time.Since(start)

		if !found || !died || already || !escalated {
			t.Errorf("killAndWait = found %v died %v alreadyExited %v escalated %v, want true true false true", found, died, already, escalated)
		}
		if took < exitDrainGrace {
			t.Errorf("killAndWait answered after %s, want it to wait for the drain grace of %s", took, exitDrainGrace)
		}
		log := buf.String()
		wantLinesInOrder(t, log,
			"[process.Manager] KillAndWait "+id+": group kill failed: TerminateProcess: Access is denied.",
			"[process.Manager] Process "+id+": pipe drain grace expired (grandchild holding stdio?); force-closing",
			exitLineOf(id)+", asked to end by this daemon (client)")
		for _, w := range []string{
			"[process.Manager] stdout read error for process " + id + ": read |0: file already closed\n",
			"[process.Manager] stderr read error for process " + id + ": read |0: file already closed\n",
		} {
			if !strings.Contains(log, w) {
				t.Errorf("log lacks %q\nlog:\n%s", w, log)
			}
		}
		// A child left alive by another test can add its own exit line after this one,
		// so the check takes the exit lines of id and not the end of the log.
		if w, lines := exitLineOf(id)+", asked to end by this daemon (client)", exitLinesOf(log, id); len(lines) != 1 || lines[0] != w {
			t.Errorf("exit lines of %s = %q, want only %q\nlog:\n%s", id, lines, w, log)
		}
		if !processAlive(t, gpid) {
			t.Errorf("grandchild %d ended", gpid)
		}
	})

	t.Run("kill", func(t *testing.T) {
		const id = "kp-kill"
		m := newTestProcManager(t)
		p, gpid := spawnTree(t, m, id, "tree-stdio")
		buf := captureLogBuf(t)

		start := time.Now()
		_ = m.kill(id, "")
		if replied := time.Since(start); replied > 500*time.Millisecond {
			t.Errorf("kill returned after %s, want at once", replied)
		}
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
			t.Fatal("no exit of the managed process")
		}
		if took := time.Since(start); took < exitDrainGrace {
			t.Errorf("the exit came after %s, want it after the drain grace of %s", took, exitDrainGrace)
		}
		log := buf.String()
		if strings.Contains(log, "group kill failed") {
			t.Errorf("log = %q, want no group kill line for process.kill", log)
		}
		wantLinesInOrder(t, log,
			"[process.Manager] Process "+id+": pipe drain grace expired (grandchild holding stdio?); force-closing",
			exitLineOf(id)+", asked to end by this daemon (client)")
		// A child left alive by another test can add its own exit line after this one,
		// so the check takes the exit lines of id and not the end of the log.
		if w, lines := exitLineOf(id)+", asked to end by this daemon (client)", exitLinesOf(log, id); len(lines) != 1 || lines[0] != w {
			t.Errorf("exit lines of %s = %q, want only %q\nlog:\n%s", id, lines, w, log)
		}
		if !processAlive(t, gpid) {
			t.Errorf("grandchild %d ended", gpid)
		}
	})
}
