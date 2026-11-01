package main

import (
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The ssh door of the checkout in the state shadow (gitsshdoor.go), with the
// rows of be1c1ca5 (Linux and macOS VMs).

// doorNames are the five names of the door, in the measured order.
var doorNames = []string{"GIT_SSH_COMMAND", "CLAUDE_SSH_PERSONS_SSH", "CLAUDE_SSH_PERSONS_ENV", "CLAUDE_SSH_DOOR_MARK", "GIT_SSH_VARIANT"}

// unsetDaemonSSH removes the daemon's own ssh variables for the test.
func unsetDaemonSSH(t *testing.T) {
	t.Helper()
	for _, k := range []string{"GIT_SSH_COMMAND", "GIT_SSH", "GIT_SSH_VARIANT"} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
}

// readTreeCall is the read-tree call of calls.
func readTreeCall(t *testing.T, calls []envCall) envCall {
	t.Helper()
	for _, c := range calls {
		if slices.Contains(c.argv, "read-tree") {
			return c
		}
	}
	t.Fatal("no read-tree call")
	return envCall{}
}

// doorRead reports whether calls hold the config read of the door.
func doorRead(calls []envCall) bool {
	return slices.ContainsFunc(calls, func(c envCall) bool {
		return argvEndsWith(c.argv, []string{"config", "-z", "--get-regexp", sshDoorRegexp})
	})
}

// doorEntries is the door entries of env, in the order of env.
func doorEntries(env []string) []string {
	var out []string
	for _, kv := range env {
		if slices.Contains(doorNames, envName(kv)) {
			out = append(out, kv)
		}
	}
	return out
}

// TestCheckoutSSHDoorEnv: the read-tree call gets the five entries of the
// door right after LANGUAGE=C, after one config read in the new worktree. A repository's own
// `core.sshCommand` and `ssh.variant` never reach the person's ssh slot: the
// read names the settings copy, and plain ssh is the person's command. A value
// of the user's own config does, and an unknown program name gets no
// GIT_SSH_VARIANT.
func TestCheckoutSSHDoorEnv(t *testing.T) {
	door := "GIT_SSH_COMMAND=" + sshDoorScript
	mark := "CLAUDE_SSH_DOOR_MARK=" + sshDoorMark()
	for _, row := range []struct {
		name, repo, user string
		daemon           []string
		want             []string
	}{
		{"only a repository value", "[core]\n\tsshCommand = /no/such/m-repo.sh\n[ssh]\n\tvariant = simple\n", "", nil,
			[]string{door, "CLAUDE_SSH_PERSONS_SSH='ssh'", "CLAUDE_SSH_PERSONS_ENV=", mark, "GIT_SSH_VARIANT=ssh"}},
		{"a value of the user config", "[core]\n\tsshCommand = /no/such/m-repo.sh\n", "[core]\n\tsshCommand = /no/such/m-home.sh\n", nil,
			[]string{door, "CLAUDE_SSH_PERSONS_SSH=/no/such/m-home.sh", "CLAUDE_SSH_PERSONS_ENV=", mark}},
		{"ssh with an option in the user config", "", "[core]\n\tsshCommand = /usr/bin/ssh -o BatchMode=yes\n", nil,
			[]string{door, "CLAUDE_SSH_PERSONS_SSH=/usr/bin/ssh -o BatchMode=yes", "CLAUDE_SSH_PERSONS_ENV=", mark, "GIT_SSH_VARIANT=ssh"}},
		{"ssh.variant simple in the user config", "", "[core]\n\tsshCommand = /usr/bin/ssh -o BatchMode=yes\n[ssh]\n\tvariant = simple\n", nil,
			[]string{door, "CLAUDE_SSH_PERSONS_SSH=/usr/bin/ssh -o BatchMode=yes", "CLAUDE_SSH_PERSONS_ENV=", mark}},
		{"plink of the daemon", "", "", []string{"GIT_SSH_COMMAND", "plink.exe -batch"},
			[]string{door, "CLAUDE_SSH_PERSONS_SSH=plink.exe -batch", "CLAUDE_SSH_PERSONS_ENV=1", mark, "GIT_SSH_VARIANT=putty"}},
		{"an unknown command of the daemon", "", "", []string{"GIT_SSH_COMMAND", "/no/such/logssh"},
			[]string{door, "CLAUDE_SSH_PERSONS_SSH=/no/such/logssh", "CLAUDE_SSH_PERSONS_ENV=1", mark}},
		{"GIT_SSH of the daemon", "", "", []string{"GIT_SSH", "plink.exe"},
			[]string{door, "CLAUDE_SSH_PERSONS_SSH='plink.exe'", "CLAUDE_SSH_PERSONS_ENV=", mark, "GIT_SSH_VARIANT=putty"}},
	} {
		t.Run(row.name, func(t *testing.T) {
			unsetDaemonSSH(t)
			if row.daemon != nil {
				t.Setenv(row.daemon[0], row.daemon[1])
			}
			f := newCheckoutRuleFixture(t)
			f.addConfig(t, row.repo)
			if row.user != "" {
				outside(t, row.user)
			}
			raw, calls := f.create(t, nil)
			if !strings.Contains(raw, `"success":true`) {
				t.Fatalf("git.worktree_create = %s", raw)
			}
			if !doorRead(calls) {
				t.Error("no config read of the door")
			}
			env := readTreeCall(t, calls).env
			got := doorEntries(env)
			want := row.want
			if runtime.GOOS != "windows" {
				if n := len(want) + 1; len(env) < n || !slices.Equal(env[len(env)-n:], append([]string{"LANGUAGE=C"}, want...)) {
					t.Errorf("read-tree env does not end with LANGUAGE=C and the door entries: %q", env[max(0, len(env)-n):])
				}
			} else {
				// Go sorts the block of a child by name on Windows (D23).
				got, want = slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))
			}
			if !slices.Equal(got, want) {
				t.Errorf("door entries of read-tree =\n%q\nwant\n%q", got, want)
			}
		})
	}
}

// TestCheckoutSSHDoorOff: with "shadowDirOff":true there is no settings copy,
// no config read of the door and no door in the read-tree call (claustrum's
// own, not measured).
func TestCheckoutSSHDoorOff(t *testing.T) {
	unsetDaemonSSH(t)
	f := newCheckoutRuleFixture(t)
	raw, calls := f.create(t, map[string]any{"shadowDirOff": true})
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s", raw)
	}
	if doorRead(calls) {
		t.Error("a config read of the door ran")
	}
	if got := doorEntries(readTreeCall(t, calls).env); len(got) != 0 {
		t.Errorf("door entries of read-tree = %q, want none", got)
	}
}

// TestCheckoutSSHDoorReadFails: a config read of the door that exits 1 finds
// no match, and the create goes on with plain ssh. Exit 2 or 128 fails the
// create with the measured text before the read-tree call, and the rollback
// runs.
func TestCheckoutSSHDoorReadFails(t *testing.T) {
	for _, row := range []struct {
		exit, want string
	}{
		{"1", ""},
		{"2", "git worktree add failed (checkout): Claude could not read your own git config on the remote machine (exit status 2). Run git config --list there to see the problem."},
		{"128", "git worktree add failed (checkout): Claude could not read your own git config on the remote machine (exit status 128). Run git config --list there to see the problem."},
	} {
		t.Run("exit "+row.exit, func(t *testing.T) {
			unsetDaemonSSH(t)
			f := newCheckoutRuleFixture(t)
			outside(t, "[core]\n\tsshCommand = /no/such/m-home.sh\n")
			t.Setenv("CLAUSTRUM_GITSTUB_MATCH2", "--get-regexp")
			t.Setenv("CLAUSTRUM_GITSTUB_EXIT2", row.exit)
			t.Setenv("CLAUSTRUM_GITSTUB_STDERR2", "boom\n")
			raw, calls := f.create(t, nil)
			if row.want != "" {
				if want := failedFrame(t, row.want); raw != want {
					t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
				}
				if ranReadTree(calls) {
					t.Error("a read-tree call ran")
				}
				f.wantGone(t)
				return
			}
			if !strings.Contains(raw, `"success":true`) {
				t.Fatalf("git.worktree_create = %s", raw)
			}
			env := readTreeCall(t, calls).env
			if v, _ := lookupExact(env, "CLAUDE_SSH_PERSONS_SSH"); v != "'ssh'" {
				t.Errorf("CLAUDE_SSH_PERSONS_SSH = %q, want 'ssh'", v)
			}
		})
	}
}

// TestCheckoutSSHDoorRefusalText: a failed checkout whose stderr holds the
// mark of the door answers the fixed text of 194 bytes, and the rollback
// runs. The stderr without the mark keeps the text of git.
func TestCheckoutSSHDoorRefusalText(t *testing.T) {
	if len(sshDoorRefusedText) != 194 {
		t.Fatalf("the text has %d bytes, want 194", len(sshDoorRefusedText))
	}
	for _, row := range []struct {
		name, stderr, want string
	}{
		{"the mark", sshDoorRefusalLine + " $CLAUDE_SSH_DOOR_MARK\\nerror: failed to fetch some objects\\n", sshDoorRefusedText},
		{"another mark", sshDoorRefusalLine + " 0000000000000000\\n", sshDoorRefusalLine + " 0000000000000000"},
	} {
		t.Run(row.name, func(t *testing.T) {
			unsetDaemonSSH(t)
			f := newCheckoutRuleFixture(t)
			slowGit(t, "read-tree", "fail", 0, row.stderr, "")
			t.Setenv("CLAUSTRUM_GITSTUB_EXIT", "128")
			t.Setenv("CLAUSTRUM_GITSTUB_EXPAND", "1")
			raw, _ := f.create(t, nil)
			if want := failedFrame(t, "git worktree add failed (checkout): "+row.want); raw != want {
				t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
			}
			f.wantGone(t)
		})
	}
}

// TestCheckoutSSHDoorReadStops: the time limit of the git calls stops a held
// config read of the door. The create answers the stop as it answers a
// stopped first listing, not with the own-config text, and rolls back
// (claustrum's own, not measured). The limit is shortened here.
func TestCheckoutSSHDoorReadStops(t *testing.T) {
	unsetDaemonSSH(t)
	f := newCheckoutRuleFixture(t)
	t.Setenv("CLAUSTRUM_GITSTUB_MATCH2", "--get-regexp")
	t.Setenv("CLAUSTRUM_GITSTUB_MS2", "30000")
	shortGitLimit(t, testCallLimit)
	raw, calls := f.create(t, nil)
	if want := failedFrame(t, "git worktree add failed (checkout): git took too long: Claude stopped git config after 2 minutes; try again"); raw != want {
		t.Errorf("git.worktree_create = %s\nwant %s", raw, want)
	}
	if ranReadTree(calls) {
		t.Error("a read-tree call ran")
	}
	f.wantGone(t)
}
