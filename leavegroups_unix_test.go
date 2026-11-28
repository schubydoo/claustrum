//go:build linux || darwin

package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/schubydoo/claustrum/internal/supervisor"
	"github.com/schubydoo/claustrum/internal/wirelog"
)

// TestStartLeaveGroups: only the exact value 1 turns the end off, logs the line and
// takes the feature out. Every value leaves the environment (87375cc7, measured on
// Linux and macOS). The feature is the last name while the rule is on.
func TestStartLeaveGroups(t *testing.T) {
	const line = "[daemon] what an exited launcher leaves in its process group lives on: CLAUDE_SSH_LEAVE_GROUPS is set"
	for _, tc := range []struct {
		value string
		set   bool
		leave bool
	}{{"1", true, true}, {"0", true, false}, {"yes", true, false}, {"", true, false}, {" 1", true, false}, {"", false, false}} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv(leaveGroupsEnv, "placeholder")
			if tc.set {
				os.Setenv(leaveGroupsEnv, tc.value)
			} else {
				os.Unsetenv(leaveGroupsEnv)
			}
			var buf bytes.Buffer
			oldW, oldF := log.Writer(), log.Flags()
			log.SetOutput(&buf)
			log.SetFlags(0)
			t.Cleanup(func() { log.SetOutput(oldW); log.SetFlags(oldF) })
			m := supervisor.NewManager()
			t.Cleanup(m.Close)
			startLeaveGroups(m)
			if _, still := os.LookupEnv(leaveGroupsEnv); still {
				t.Error("the variable stays in the daemon environment")
			}
			if m.LeaveGroups != tc.leave {
				t.Errorf("LeaveGroups = %v, want %v", m.LeaveGroups, tc.leave)
			}
			if got := strings.Count(buf.String(), line); (got == 1) != tc.leave || got > 1 {
				t.Errorf("the line appears %d times, leave %v", got, tc.leave)
			}
			features := (&server{procs: m}).featuresFor()
			// 13047a51 lists process.spawn.host_creds_pipe last on Linux, also with
			// the variable set (measured on Linux).
			if n := len(hostCredsFeatures); n > 0 {
				if features[len(features)-1] != hostCredsFeature {
					t.Errorf("last feature = %q, want %s", features[len(features)-1], hostCredsFeature)
				}
				features = features[:len(features)-n]
			}
			has := slices.Contains(features, launcherExitFeature)
			if has == tc.leave {
				t.Errorf("feature listed = %v with leave %v", has, tc.leave)
			}
			if !tc.leave && features[len(features)-1] != launcherExitFeature {
				t.Errorf("last feature = %q", features[len(features)-1])
			}
			if tc.leave && features[len(features)-1] != "server.instance_id" {
				t.Errorf("last feature = %q, want server.instance_id", features[len(features)-1])
			}
		})
	}
}

// TestServerPassesKeepChildrenToTheManager: -keep-children reaches the manager, so a
// shutdown sends no signal to the group of an exited launcher (claustrum's own).
func TestServerPassesKeepChildrenToTheManager(t *testing.T) {
	for _, keep := range []bool{false, true} {
		sock := filepath.Join(shortTempDir(t), "s.sock")
		s, err := newServerOnSocket(sock, "tok", "", wirelog.Options{}, keep, false)
		if err != nil {
			t.Fatalf("newServerOnSocket: %v", err)
		}
		t.Cleanup(func() { s.procs.KillAll() })
		t.Cleanup(func() { _ = s.ln.Close() })
		if s.procs.KeepChildren != keep {
			t.Errorf("KeepChildren = %v, want %v", s.procs.KeepChildren, keep)
		}
	}
}
