//go:build unix

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The ssh command and the trace entries on each git call of the five git
// methods, in the state shadow and with "shadowDirOff":true. A call with
// GIT_NO_LAZY_FETCH=1 and GIT_ASKPASS= gets GIT_SSH_COMMAND=false, except a
// call of a create or a remove with the member, which keeps the daemon's
// value. Every other call keeps the daemon's value too. be1c1ca5 does so on
// Linux. Each call has the three trace entries with the value 0.
func TestGitCallSSHAndTracePerState(t *testing.T) {
	requireGit(t)
	for _, state := range []shapeState{stateShadow, stateOff} {
		t.Run(state.String(), func(t *testing.T) {
			f := newShapeFixture(t, state)
			t.Setenv("GIT_SSH_COMMAND", "daemon-ssh")
			for _, n := range gitTraceNames {
				t.Setenv(n, filepath.Join(t.TempDir(), "trace"))
			}
			for _, c := range []struct {
				method string
				params map[string]any
				ok     string
			}{
				{"git.info", map[string]any{"path": f.repo}, `"isRepo":true`},
				{"git.list_branches", map[string]any{"path": f.repo}, `"result"`},
				{"git.status", map[string]any{"path": f.wt, "baseRepo": f.repo}, `"isRepo":true`},
				{"git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": f.leaf}, `"success":true`},
				{"git.worktree_remove", map[string]any{"baseRepo": f.repo, "worktreePath": f.leaf, "branchName": "w1"}, `"success":true`},
			} {
				if state == stateOff {
					c.params["shadowDirOff"] = true
				}
				raw, calls := f.call(t, c.method, c.params)
				if !strings.Contains(raw, c.ok) {
					t.Fatalf("%s = %s", c.method, raw)
				}
				keeps := state == stateOff && (c.method == "git.worktree_create" || c.method == "git.worktree_remove")
				pairs := 0
				for i, call := range calls {
					what := c.method + " call " + strings.Join(call.argv, " ")
					var ssh []string
					for _, kv := range call.env {
						if envName(kv) == "GIT_SSH_COMMAND" {
							ssh = append(ssh, kv)
						}
					}
					want := "GIT_SSH_COMMAND=daemon-ssh"
					if slices.Contains(call.env, "GIT_NO_LAZY_FETCH=1") && slices.Contains(call.env, "GIT_ASKPASS=") {
						pairs++
						if !keeps {
							want = sshOffEntry
						}
					}
					if !slices.Equal(ssh, []string{want}) {
						t.Errorf("%s (%d): %q, want %q", what, i+1, ssh, want)
					}
					for _, n := range gitTraceNames {
						var vals []string
						for _, kv := range call.env {
							if k, v, _ := strings.Cut(kv, "="); k == n {
								vals = append(vals, v)
							}
						}
						if !slices.Equal(vals, []string{"0"}) {
							t.Errorf("%s (%d): %s = %q, want one entry 0", what, i+1, n, vals)
						}
					}
				}
				if pairs == 0 {
					t.Errorf("%s: no call has GIT_NO_LAZY_FETCH=1 and GIT_ASKPASS=", c.method)
				}
			}
		})
	}
}

// The `git version` run after a failed listing carries the heavy profile, so
// it gets GIT_SSH_COMMAND=false in place of the daemon's value. The light
// profile has no GIT_ASKPASS=, so that run keeps the daemon's value.
func TestVersionRunAfterListingSSH(t *testing.T) {
	t.Setenv("GIT_SSH_COMMAND", "daemon-ssh")
	bin := stubGit(t, gitStub{version: "git version 2.43.0\n"})
	for _, c := range []struct {
		heavy bool
		want  string
	}{
		{true, sshOffEntry},
		{false, "GIT_SSH_COMMAND=daemon-ssh"},
	} {
		gitVersionRun(gitUse{}, c.heavy)
		data, err := os.ReadFile(filepath.Join(bin, "version.env"))
		if err != nil {
			t.Fatal(err)
		}
		var ssh []string
		for _, kv := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			if envName(kv) == "GIT_SSH_COMMAND" {
				ssh = append(ssh, kv)
			}
		}
		if !slices.Equal(ssh, []string{c.want}) {
			t.Errorf("heavy %v: %q, want %q", c.heavy, ssh, c.want)
		}
	}
}

// The settings gate of a request gives it a new use in the state shadow and in
// the state fallback-git. The mark of a create or a remove stays on that use.
func TestSettingsGateKeepsTheSSHMark(t *testing.T) {
	requireGit(t)
	for _, state := range []shapeState{stateShadow, stateFallback} {
		t.Run(state.String(), func(t *testing.T) {
			f := newShapeFixture(t, state)
			req := &request{shadow: f.s.gitShadow}
			req.use.sshKept = func() bool { return true }
			if msg, noRepo := req.settingsGate(requestGitDirTrust(f.repo, false), false, f.repo, true); msg != "" || noRepo {
				t.Fatalf("gate refused: %q, noRepo %v", msg, noRepo)
			}
			req.runAtReply()
			if state == stateShadow && !req.use.shadow() || state == stateFallback && !req.use.inFolder() {
				t.Fatalf("the gate gave no new use: %+v", req.use)
			}
			if req.use.sshKept == nil {
				t.Error("the new use lost the mark of a create or a remove")
			}
		})
	}
}

// The pair of a remove for a folder that no registration names carries the
// heavy profile. In the state shadow it gets GIT_SSH_COMMAND=false. With the
// member the remove keeps the daemon's value there.
func TestRemovePairSSH(t *testing.T) {
	requireGit(t)
	for _, state := range []shapeState{stateShadow, stateOff} {
		t.Run(state.String(), func(t *testing.T) {
			f := newShapeFixture(t, state)
			t.Setenv("GIT_SSH_COMMAND", "daemon-ssh")
			stray := filepath.Join(f.repo, ".claude", "worktrees", "stray")
			if err := os.MkdirAll(stray, 0o755); err != nil {
				t.Fatal(err)
			}
			params := map[string]any{"baseRepo": f.repo, "worktreePath": stray}
			if state == stateOff {
				params["shadowDirOff"] = true
			}
			raw, calls := f.call(t, "git.worktree_remove", params)
			if raw != removeOK {
				t.Fatalf("git.worktree_remove = %s", raw)
			}
			want := map[shapeState]string{stateShadow: sshOffEntry, stateOff: "GIT_SSH_COMMAND=daemon-ssh"}[state]
			seen := 0
			for _, c := range calls {
				if !slices.Contains(c.argv, "--show-toplevel") || !slices.Contains(c.argv, "--work-tree="+f.repo) {
					continue
				}
				seen++
				var ssh []string
				for _, kv := range c.env {
					if envName(kv) == "GIT_SSH_COMMAND" {
						ssh = append(ssh, kv)
					}
				}
				if !slices.Equal(ssh, []string{want}) {
					t.Errorf("the pair: %q, want %q", ssh, want)
				}
			}
			if seen == 0 {
				t.Fatal("no pair ran")
			}
		})
	}
}
