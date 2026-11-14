package supervisor

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLogID: an id of 8 bytes stays whole, and a longer id becomes its first 8
// bytes and "…" (c9125eb8 and 4c111e86, measured on Linux, macOS and Windows).
// claustrum's own, not measured: an id with a byte above 0x7f.
func TestLogID(t *testing.T) {
	for _, c := range []struct{ id, want string }{
		{"", ""},
		{"s1_plain", "s1_plain"},
		{"b_control", "b_contro…"},
		{"id1-b12_rm_ignore_term", "id1-b12_…"},
		{"abcdefgé", "abcdefg…"},
		{"abcdefgéh", "abcdefg…"},
		{"abcdefghé", "abcdefgh…"},
	} {
		if got := LogID(c.id); got != c.want {
			t.Errorf("LogID(%q) = %q, want %q", c.id, got, c.want)
		}
	}
}

// TestSpawnLogCutsLongID: the started, streaming and exit lines show a long id cut
// to 8 bytes and "…", and so do the failed-start lines of a given-up id and of a
// missing cwd (c9125eb8 and 4c111e86, measured on Linux, macOS and Windows).
// Mutation: drop LogID at each of these lines.
func TestSpawnLogCutsLongID(t *testing.T) {
	buf := captureLogBuf(t)
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, frames := pipeConn(t)
	echo, env := helperCommand(t, "echo")
	if _, err := m.Spawn(c, "logid-long-1", echo, []string{"hi"}, "", env, false); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	deadline := time.After(10 * time.Second)
	for exited := false; !exited; {
		select {
		case f := <-frames:
			exited = f.Stream == "exit"
		case <-deadline:
			t.Fatal("no exit frame")
		}
	}
	m.GiveUpUnknown("logid-gone-1")
	if _, err := m.Spawn(c, "logid-gone-1", echo, nil, "", env, false); err != errGivenUpID {
		t.Fatalf("spawn of a given-up id = %v, want the given-up refusal", err)
	}
	gone := filepath.Join(t.TempDir(), "gone")
	if _, err := m.Spawn(c, "logid-cwd-1", echo, nil, gone, env, false); err == nil {
		t.Fatal("spawn in a missing cwd started")
	}
	var mine []string
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, "logid-") {
			mine = append(mine, l)
		}
	}
	got := strings.Join(mine, "\n")
	for _, want := range []string{
		"[process.Manager] Process logid-lo… started, PID=",
		"[process.Manager] Starting stdout streaming for process logid-lo…",
		"[process.Manager] Starting stderr streaming for process logid-lo…",
		"[process.Manager] Process logid-lo… exited with code 0",
		"[process.Manager] Failed to start process logid-go…: this id was asked after",
		"[process.Manager] Failed to start process logid-cw…: chdir " + gone + ": ",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing log line %q\n--- own lines ---\n%s", want, got)
		}
	}
	if strings.Contains(got, "logid-long-1") || strings.Contains(got, "logid-gone-1") || strings.Contains(got, "logid-cwd-1") {
		t.Errorf("a whole long id in a cut line\n--- own lines ---\n%s", got)
	}
}
