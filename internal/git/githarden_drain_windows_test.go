package git

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// On Windows a read-tree call that the deadline stops gets no stdout drain.
// 87375cc7 answers right at the stop while a process holds only the stdout of
// git, and it waits for a process that holds stderr too (measured on Windows).
// The stub starts a child that holds its output for 10 s, and the deadline
// fires once that child runs. The drain cap is longer than the hold.
func TestStoppedCheckoutSkipsStdoutDrainOnWindows(t *testing.T) {
	const hold = 10 * time.Second
	setDuration(t, &worktreeCreateDrainCap, 30*time.Second)
	for _, tc := range []struct {
		name       string
		stdoutOnly bool
	}{
		{"stdout only", true},
		{"stdout and stderr", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWTFixture(t, false)
			h := newTestGitDeps()
			installGitSlowStub(t, f.realGit)
			slowGit(t, "read-tree", "holdwait", hold, "", "")
			t.Setenv("CLAUSTRUM_GITSTUB_WAITMS", "60000")
			if tc.stdoutOnly {
				t.Setenv("CLAUSTRUM_GITSTUB_HOLDSTDOUT", "1")
			}
			endHeldDescendant(t)
			self := filepath.Join(t.TempDir(), "self")
			t.Setenv("CLAUSTRUM_GITSTUB_HOLDSELF", self)
			fired := deadlineAt(t, func() bool { _, err := os.Stat(self); return err == nil }, nil)
			start := time.Now()
			raw, _ := f.create(t, h, "w1", "", markerTimeoutMs)
			replied := time.Now()
			want := `"error":"git worktree add timed out after ` + strconv.Itoa(markerTimeoutMs) + `ms (deadline expired during the checkout)`
			if !strings.Contains(raw, want) {
				t.Fatalf("reply = %s, want %s", raw, want)
			}
			if fired().IsZero() || fired().Before(start) {
				t.Fatalf("the deadline did not fire during the create")
			}
			wait := replied.Sub(fired())
			if tc.stdoutOnly && wait >= hold/2 {
				t.Errorf("reply %s after the deadline, want it before the holder of stdout ends (%s)", wait, hold)
			}
			if !tc.stdoutOnly && wait < hold-2*time.Second {
				t.Errorf("reply %s after the deadline, want it after the holder of stderr ends (%s)", wait, hold)
			}
		})
	}
}
