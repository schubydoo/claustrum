//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runDoor runs the door as git and git-lfs run GIT_SSH_COMMAND, with a fake
// ssh of the test as the person's command, and variant as GIT_SSH_VARIANT. It
// returns the words that the fake ssh got (or "" when it did not run), the
// CLAUDE_SSH_ entries of its environment, the stderr and the exit code.
func runDoor(t *testing.T, variant string, args ...string) (ran, names, stderr string, code int) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "ssh.log")
	envLog := filepath.Join(dir, "env.log")
	fake := filepath.Join(dir, "fake-ssh")
	writeFile(t, fake, "#!/bin/sh\nfor a; do printf '[%s]' \"$a\"; done > \""+log+"\"\nenv | grep '^CLAUDE_SSH_' | sort > \""+envLog+"\"\n", 0o755)
	cmd := exec.Command("sh", append([]string{"-c", sshDoorScript + ` "$@"`, sshDoorScript}, args...)...)
	env := []string{"CLAUDE_SSH_PERSONS_SSH=" + shellQuoted(fake), "CLAUDE_SSH_PERSONS_ENV=1",
		"CLAUDE_SSH_DOOR_MARK=0123456789abcdef", "CLAUDE_SSH_DAEMON_CHILD=1", "GIT_SSH_VARIANT=" + variant}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CLAUDE_SSH_") && !strings.HasPrefix(kv, "GIT_SSH_VARIANT=") {
			env = append(env, kv)
		}
	}
	cmd.Env = env
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	e, _ := os.ReadFile(envLog)
	return string(b), string(e), errBuf.String(), code
}

// TestSSHDoorCases: the requests that reach ssh and the refused ones, with the
// command as one word as git-lfs sends it (be1c1ca5, measured on Linux), and
// the odd URLs of the rows on Linux and macOS. A refusal writes the line with
// the mark, exits 1 and starts no ssh. The person's ssh sees
// CLAUDE_SSH_DAEMON_CHILD and none of the three names of the door (measured on
// Linux).
func TestSSHDoorCases(t *testing.T) {
	refusal := sshDoorRefusalLine + " 0123456789abcdef\n"
	multiplex := []string{"-oControlMaster=yes", "-oControlPath=/tmp/sock-123/lfs.sock", "git@host", "git-lfs-transfer /a/b download"}
	for _, row := range []struct {
		name    string
		variant string
		args    []string
		pass    bool
	}{
		{"transfer download", "ssh", []string{"git@host", "git-lfs-transfer a/b download"}, true},
		{"authenticate upload", "ssh", []string{"git@host", "git-lfs-authenticate a/b upload"}, true},
		{"a port first", "ssh", []string{"-p", "2222", "git@host", "git-lfs-authenticate a/b download"}, true},
		{"the multiplex options of git-lfs", "ssh", multiplex, true},
		{"not a Git LFS request", "ssh", []string{"git@host", "ls"}, true},
		{"plain download", "ssh", []string{"git@host", "git-lfs-authenticate a/b download"}, true},
		{"the path that git-lfs on Windows sends", "ssh", []string{"git@host", "git-lfs-authenticate /a download"}, true},
		{"git with options", "ssh", []string{"-o", "SendEnv=GIT_PROTOCOL", "git@host", "git-upload-pack 'a/b'"}, true},
		{"a semicolon in the path", "ssh", []string{"git@host", "git-lfs-authenticate a;b download"}, false},
		{"a dollar in the path", "ssh", []string{"host", "git-lfs-authenticate pa$th download"}, false},
		{"the operation delete", "ssh", []string{"git@host", "git-lfs-authenticate a/b delete"}, false},
		{"a proxy command first", "ssh", []string{"-oProxyCommand=x", "git@host", "git-lfs-authenticate a/b download"}, false},
		{"a proxy command after the multiplex options", "ssh", []string{"-oControlMaster=yes", "-oProxyCommand=x", "git@host", "git-lfs-transfer /a/b download"}, false},
		{"a control path with a semicolon", "ssh", []string{"-oControlMaster=yes", "-oControlPath=/tmp/a;b", "git@host", "git-lfs-transfer /a/b download"}, false},
		{"a host with a dash", "ssh", []string{"-Fconfig", "git-lfs-authenticate a/b download"}, false},
		{"a path with a dash", "ssh", []string{"git@host", "git-lfs-authenticate -a download"}, false},
		{"an extra word", "ssh", []string{"git@host", "git-lfs-transfer", "git-lfs-authenticate a/b download"}, false},
		{"four spaces in the command", "ssh", []string{"git@host", "git-lfs-authenticate    a/b download"}, false},
		{"separate words", "ssh", []string{"git@host", "git-lfs-transfer", "a/b", "download"}, false},
		// The putty options, claustrum's own (not measured).
		{"a putty port", "putty", []string{"-batch", "-P", "2222", "git@host", "git-lfs-authenticate a/b download"}, true},
		{"a putty port for ssh", "ssh", []string{"-P", "2222", "git@host", "git-lfs-authenticate a/b download"}, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			ran, names, stderr, code := runDoor(t, row.variant, row.args...)
			if row.pass {
				want := "[" + strings.Join(row.args, "][") + "]"
				if ran != want || code != 0 || stderr != "" {
					t.Errorf("ssh got %q, exit %d, stderr %q; want %q, exit 0", ran, code, stderr, want)
				}
				if names != "CLAUDE_SSH_DAEMON_CHILD=1\n" {
					t.Errorf("the CLAUDE_SSH_ entries of ssh = %q, want CLAUDE_SSH_DAEMON_CHILD=1 alone", names)
				}
				return
			}
			if ran != "" || code != 1 || stderr != refusal {
				t.Errorf("ssh got %q, exit %d, stderr %q; want no ssh, exit 1 and %q", ran, code, stderr, refusal)
			}
		})
	}
}
