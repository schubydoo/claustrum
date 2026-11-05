//go:build linux

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/schubydoo/claustrum/internal/supervisor"
)

// TestSocketSpawnTrampolineMarkers is the end-to-end proof: a daemon on a
// run/<clientId>/rpc.sock socket spawns a child through the exec-child trampoline,
// and the child's environment carries CLAUDE_SSH_CHILD=<pid>:<ticks> and
// CLAUDE_SSH_RUN_DIR, while a Go-runtime var round-trips through the hold/restore.
// It spawns the printenv helper (which prints one requested var) once per marker and
// reads the value off the child's stdout stream. A mutant that skipped the trampoline
// would leave CLAUDE_SSH_CHILD empty; one that dropped the run-dir marker would leave
// CLAUDE_SSH_RUN_DIR empty; one that failed to restore held env would drop GODEBUG.
func TestSocketSpawnTrampolineMarkers(t *testing.T) {
	base, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	const clientID = "c0ffee01"
	runDir := filepath.Join(base, "run", clientID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(runDir, "rpc.sock")
	s, _ := newRunningServerAt(t, sock)
	if s.procs.RunDir != runDir {
		t.Fatalf("procs.RunDir = %q, want %q (trampoline would not engage)", s.procs.RunDir, runDir)
	}
	cl := dial(t, sock)

	// printenv prints "<name>=<value>\n" for its first arg. Spawn it through the
	// trampoline once per env var of interest and return the child's stdout.
	printenv := func(id, procID, name string) string {
		exe, env := helperCommand(t, "printenv")
		env["GODEBUG"] = "madvdontneed=1" // held on the way out, restored in the child
		body, merr := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": id, "method": "process.spawn", "auth": testToken,
			"params": map[string]any{"id": procID, "command": exe, "args": []string{name}, "env": env},
		})
		if merr != nil {
			t.Fatal(merr)
		}
		cl.call(string(body))
		return streamBytes(t, cl.waitExit(procID), "stdout")
	}

	if got := printenv("1", "child", supervisor.EnvChildMarker); !regexp.MustCompile(`^CLAUDE_SSH_CHILD=\d+:\d+\n$`).MatchString(got) {
		t.Errorf("child %s = %q, want <pid>:<ticks>", supervisor.EnvChildMarker, got)
	}
	if got := printenv("2", "rundir", supervisor.EnvRunDir); got != supervisor.EnvRunDir+"="+runDir+"\n" {
		t.Errorf("child %s = %q, want %q", supervisor.EnvRunDir, got, supervisor.EnvRunDir+"="+runDir+"\n")
	}
	if got := printenv("3", "godebug", "GODEBUG"); got != "GODEBUG=madvdontneed=1\n" {
		t.Errorf("child GODEBUG = %q, want the restored value (hold/unhold round-trip)", got)
	}
}

// TestSocketSpawnTrampolineEdgeCases pins the two frame divergences the wire-byte
// review caught, measured against 19f30c46 on Linux. Class A: a resolvable-but-not-
// execve-able command (a +x file that is not an executable format) must return the
// identical -32603 "fork/exec …: exec format error" frame, NOT success + exit 127 —
// the trampoline reports the target's exec failure over its error pipe. Class B: a
// relative command run under a cwd must still be trampolined (the child carries
// CLAUDE_SSH_CHILD), because the trampoline resolves it against the inherited cwd.
func TestSocketSpawnTrampolineEdgeCases(t *testing.T) {
	base, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	runDir := filepath.Join(base, "run", "c0ffee01")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, sock := newRunningServerAt(t, filepath.Join(runDir, "rpc.sock"))
	if s.procs.RunDir == "" {
		t.Fatal("runDir empty; trampoline would not engage")
	}
	cl := dial(t, sock)
	spawn := func(id, procID, command string, args []string, cwd string) json.RawMessage {
		params := map[string]any{"id": procID, "command": command, "args": args}
		if cwd != "" {
			params["cwd"] = cwd
		}
		body, merr := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": id, "method": "process.spawn", "auth": testToken, "params": params,
		})
		if merr != nil {
			t.Fatal(merr)
		}
		return cl.call(string(body))
	}

	// Class A: a +x file that is not an executable format -> exec format error frame.
	noexec := filepath.Join(base, "noexec")
	if err := os.WriteFile(noexec, []byte("this is not a program\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := string(spawn("1", "noexecA", noexec, []string{}, ""))
	if !strings.Contains(got, `"code":-32603`) || !strings.Contains(got, "fork/exec") || !strings.Contains(got, "exec format error") {
		t.Errorf("non-executable-format spawn = %s, want -32603 fork/exec … exec format error", got)
	}

	// Class B: a relative command under a cwd -> trampolined; child prints its env.
	reldir := filepath.Join(base, "reldir")
	if err := os.MkdirAll(reldir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reldir, "prog"), []byte("#!/bin/sh\nenv\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if r := string(spawn("2", "relB", "./prog", []string{}, reldir)); !strings.Contains(r, `"success":true`) {
		t.Fatalf("relative-command spawn = %s, want success", r)
	}
	if env := streamBytes(t, cl.waitExit("relB"), "stdout"); !regexp.MustCompile(`(?m)^CLAUDE_SSH_CHILD=\d+:\d+$`).MatchString(env) {
		t.Errorf("relative command was not trampolined: child env has no CLAUDE_SSH_CHILD:\n%s", env)
	}
}
