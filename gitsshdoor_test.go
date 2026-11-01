package main

import (
	"slices"
	"testing"
)

// TestPersonsSSH: the person's ssh command, its variant and whether it is the
// daemon's GIT_SSH_COMMAND. The sources are the daemon's GIT_SSH_COMMAND,
// `core.sshCommand` of the read, the daemon's GIT_SSH (quoted), and then
// 'ssh' (quoted).
func TestPersonsSSH(t *testing.T) {
	cfg := sshDoorSettings{command: "/x/m-home.sh", hasCommand: true}
	for _, row := range []struct {
		name             string
		env              []string
		s                sshDoorSettings
		command, variant string
		fromDaemon       bool
	}{
		{"nothing", nil, sshDoorSettings{}, "'ssh'", "ssh", false},
		{"the config", nil, cfg, "/x/m-home.sh", "", false},
		{"the daemon's command first", []string{"GIT_SSH_COMMAND=plink.exe -batch"}, cfg, "plink.exe -batch", "putty", true},
		{"an unknown daemon command", []string{"GIT_SSH_COMMAND=C:/o/r3/logssh.cmd"}, sshDoorSettings{}, "C:/o/r3/logssh.cmd", "", true},
		{"the config before GIT_SSH", []string{"GIT_SSH=/x/plink"}, cfg, "/x/m-home.sh", "", false},
		{"GIT_SSH plink", []string{"GIT_SSH=plink.exe"}, sshDoorSettings{}, "'plink.exe'", "putty", false},
		{"GIT_SSH quoted", []string{"GIT_SSH=/x/it's/ssh"}, sshDoorSettings{}, `'/x/it'\''s/ssh'`, "ssh", false},
		{"an empty daemon command", []string{"GIT_SSH_COMMAND="}, sshDoorSettings{}, "'ssh'", "ssh", false},
		{"a quoted program", []string{`GIT_SSH_COMMAND="/a b/TortoisePlink.exe" -v`}, sshDoorSettings{}, `"/a b/TortoisePlink.exe" -v`, "tortoiseplink", true},
	} {
		t.Run(row.name, func(t *testing.T) {
			command, variant, fromDaemon := personsSSH(row.env, row.s)
			if command != row.command || variant != row.variant || fromDaemon != row.fromDaemon {
				t.Errorf("personsSSH = %q, %q, %v; want %q, %q, %v", command, variant, fromDaemon, row.command, row.variant, row.fromDaemon)
			}
		})
	}
}

// TestSSHDoorEnvReplaces: the door takes the place of the daemon's
// GIT_SSH_COMMAND and of its GIT_SSH_VARIANT when it sets one. With
// `ssh.variant` set the daemon's GIT_SSH_VARIANT stays.
func TestSSHDoorEnvReplaces(t *testing.T) {
	base := []string{"A=1", "GIT_SSH_COMMAND=ssh", "GIT_SSH_VARIANT=simple", "B=2"}
	got := sshDoorEnv(base, sshDoorSettings{})
	want := []string{"A=1", "B=2", "GIT_SSH_COMMAND=" + sshDoorScript, "CLAUDE_SSH_PERSONS_SSH=ssh",
		"CLAUDE_SSH_PERSONS_ENV=1", "CLAUDE_SSH_DOOR_MARK=" + sshDoorMark(), "GIT_SSH_VARIANT=ssh"}
	if !slices.Equal(got, want) {
		t.Errorf("sshDoorEnv =\n%q\nwant\n%q", got, want)
	}
	got = sshDoorEnv([]string{"A=1", "GIT_SSH_VARIANT=simple"}, sshDoorSettings{variant: "putty", hasVariant: true})
	want = []string{"A=1", "GIT_SSH_VARIANT=simple", "GIT_SSH_COMMAND=" + sshDoorScript, "CLAUDE_SSH_PERSONS_SSH='ssh'",
		"CLAUDE_SSH_PERSONS_ENV=", "CLAUDE_SSH_DOOR_MARK=" + sshDoorMark()}
	if !slices.Equal(got, want) {
		t.Errorf("sshDoorEnv with ssh.variant =\n%q\nwant\n%q", got, want)
	}
	if m := sshDoorMark(); len(m) != 16 || m != sshDoorMark() {
		t.Errorf("mark %q: want 16 hex digits, one value per process", m)
	}
}
