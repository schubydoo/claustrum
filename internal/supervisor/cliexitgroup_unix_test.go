//go:build linux || darwin

package supervisor

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// spawnCLIGroup spawns the helper with no launcher under the given command name. The
// leader leaves a member of the given shape (hold or closed) and kind (exit or ignore)
// in its group, and exits.
func spawnCLIGroup(t *testing.T, m *Manager, id, name string, ask TempRootAsk, shape, kind string) (*Proc, int) {
	t.Helper()
	dir := t.TempDir()
	exe, env := helperCommand(t, "group-leader")
	command := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(command), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, command); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(dir, "member")
	c, _ := rawLineConn(t)
	p, err := m.SpawnWithTempRoot(c, id, command, []string{"cli", pidFile, shape, kind, "go"}, "", env, false, nil, 0, ask, nil, 0)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	t.Cleanup(func() {
		p.signalIfLive("KILL", "")
		select {
		case <-p.done:
		case <-time.After(20 * time.Second):
			t.Error("the child did not end")
		}
	})
	return p, drainMemberPid(t, pidFile)
}

// TestClaudeCodeSpawnEndsItsGroup: 13047a51 ends what an exited Claude Code child left
// in its group, for a command named ccd-cli (measured on Linux and macOS) or in a folder
// named ccd-cli (measured on Linux). A plain name and CLAUDE_SSH_LEAVE_GROUPS=1 keep the
// member (measured). claustrum also keeps the member of a pty spawn, which the reference
// ends by another rule (measured on macOS).
func TestClaudeCodeSpawnEndsItsGroup(t *testing.T) {
	for _, tc := range []struct {
		name  string
		path  string
		ask   TempRootAsk
		leave bool
		ends  bool
	}{
		{"named", "ccd-cli", TempRootAsk{}, false, true},
		{"folder", "ccd-cli/2.0.0", TempRootAsk{}, false, true},
		{"plain", "plain-leader", TempRootAsk{}, false, false},
		{"pty", "ccd-cli", TempRootAsk{Pty: true}, false, false},
		{"leave", "ccd-cli", TempRootAsk{}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestProcManager(t)
			m.LeaveGroups = tc.leave
			t.Cleanup(m.KillAll)
			p, member := spawnCLIGroup(t, m, "cc-"+tc.name, tc.path, tc.ask, "closed", "ignore")
			if p.endsGroup != tc.ends || (tc.ends && p.plainGroupEnd) {
				t.Errorf("endsGroup %v, plainGroupEnd %v, want endsGroup %v", p.endsGroup, p.plainGroupEnd, tc.ends)
			}
			awaitDrainDone(t, p)
			if tc.ends {
				awaitDrainMemberGone(t, member)
				return
			}
			if !memberLives(member) {
				t.Error("the member is gone, want it alive")
			}
		})
	}
}
