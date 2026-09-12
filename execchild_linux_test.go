//go:build linux

package main

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestExecChildRunDir pins the run-dir derivation and the trampoline gate: only a
// run/<clientId>/rpc.sock socket yields a run dir; every other shape yields "" (so
// the daemon spawns children directly, no trampoline). Measured against 19f30c46:
// CLAUDE_SSH_RUN_DIR was the socket's directory only under run/<clientId>/.
func TestExecChildRunDir(t *testing.T) {
	cases := []struct {
		sock, want string
	}{
		{"/x/run/c0ffee01/rpc.sock", "/x/run/c0ffee01"},
		{"/home/u/.claude/remote/run/abcd1234/rpc.sock", "/home/u/.claude/remote/run/abcd1234"},
		{"/tmp/cl123/s.sock", ""},           // test socket: not rpc.sock
		{"/tmp/cl123/rpc.sock", ""},         // rpc.sock but parent-of-parent is not "run"
		{"/x/notrun/c0ffee01/rpc.sock", ""}, // parent-of-parent not "run"
		{"", ""},
	}
	for _, c := range cases {
		if got := execChildRunDir(c.sock); got != filepath.FromSlash(c.want) {
			t.Errorf("execChildRunDir(%q) = %q, want %q", c.sock, got, c.want)
		}
	}
}

// TestHoldRestoreGoEnvRoundTrip proves holdGoEnv → restoreHeldEnv is the identity for
// the five Go-runtime vars (the child ends up with the daemon's values restored),
// that non-held vars pass through untouched, and that no CLAUDE_SSH_HELD_ residue
// survives — matching the measured child env, where GODEBUG round-tripped and no
// held key remained.
func TestHoldRestoreGoEnvRoundTrip(t *testing.T) {
	in := []string{
		"GODEBUG=x=1,y=2", "GOGC=50", "GOMAXPROCS=4", "GOMEMLIMIT=123MiB", "GOTRACEBACK=all",
		"PATH=/usr/bin", "CLAUDE_SSH_RUN_DIR=/x/run/c0ffee01", "FOO=bar",
	}
	held := holdGoEnv(slices.Clone(in))
	for _, name := range heldGoRuntimeEnv {
		if slices.ContainsFunc(held, func(e string) bool { return strings.HasPrefix(e, name+"=") }) {
			t.Errorf("after hold, bare %s still present: %v", name, held)
		}
		if !slices.Contains(held, heldEnvPrefix+name+"="+goVal(in, name)) {
			t.Errorf("after hold, %s%s not stashed", heldEnvPrefix, name)
		}
	}
	got := restoreHeldEnv(held)
	for _, e := range got {
		if strings.HasPrefix(e, heldEnvPrefix) {
			t.Errorf("restore left a held residue: %q", e)
		}
	}
	slices.Sort(got)
	want := slices.Clone(in)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("round-trip mismatch:\n got=%v\nwant=%v", got, want)
	}
}

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
	if s.procs.runDir != runDir {
		t.Fatalf("procs.runDir = %q, want %q (trampoline would not engage)", s.procs.runDir, runDir)
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

	if got := printenv("1", "child", envChildMarker); !regexp.MustCompile(`^CLAUDE_SSH_CHILD=\d+:\d+\n$`).MatchString(got) {
		t.Errorf("child %s = %q, want <pid>:<ticks>", envChildMarker, got)
	}
	if got := printenv("2", "rundir", envRunDir); got != envRunDir+"="+runDir+"\n" {
		t.Errorf("child %s = %q, want %q", envRunDir, got, envRunDir+"="+runDir+"\n")
	}
	if got := printenv("3", "godebug", "GODEBUG"); got != "GODEBUG=madvdontneed=1\n" {
		t.Errorf("child GODEBUG = %q, want the restored value (hold/unhold round-trip)", got)
	}
}

// TestMaybeRunExecChildExecsTarget exercises the trampoline body in-process by
// seaming syscall.Exec and os.Exit (the real Exec never returns, so this path is
// otherwise only reachable in a re-exec'd subprocess). It confirms maybeRunExecChild
// dispatches on the --exec-child arg, execs the resolved path with the target's own
// argv, stamps CLAUDE_SSH_CHILD, restores a held Go var (overriding the bare one),
// and leaves no CLAUDE_SSH_HELD_ residue.
func TestMaybeRunExecChildExecsTarget(t *testing.T) {
	var gotPath string
	var gotArgv, gotEnv []string
	oldExec, oldExit := execChildExec, osExit
	execChildExec = func(path string, argv, env []string) error {
		gotPath, gotArgv, gotEnv = path, argv, env
		return nil
	}
	osExit = func(int) {}
	t.Cleanup(func() { execChildExec, osExit = oldExec, oldExit })

	oldArgs := os.Args
	os.Args = []string{"/self", execChildFlag, "/bin/echo", "echo", "hi"}
	t.Cleanup(func() { os.Args = oldArgs })
	t.Setenv("GODEBUG", "orig")
	t.Setenv(heldEnvPrefix+"GODEBUG", "held=1")

	maybeRunExecChild()

	if gotPath != "/bin/echo" {
		t.Errorf("exec path = %q, want /bin/echo", gotPath)
	}
	if !slices.Equal(gotArgv, []string{"echo", "hi"}) {
		t.Errorf("exec argv = %v, want [echo hi]", gotArgv)
	}
	if !slices.ContainsFunc(gotEnv, func(e string) bool {
		return regexp.MustCompile(`^CLAUDE_SSH_CHILD=\d+:\d+$`).MatchString(e)
	}) {
		t.Errorf("exec env missing CLAUDE_SSH_CHILD: %v", gotEnv)
	}
	if !slices.Contains(gotEnv, "GODEBUG=held=1") || slices.Contains(gotEnv, "GODEBUG=orig") {
		t.Errorf("held GODEBUG not restored over the bare one: %v", gotEnv)
	}
	for _, e := range gotEnv {
		if strings.HasPrefix(e, heldEnvPrefix) {
			t.Errorf("HELD residue in exec env: %q", e)
		}
	}
}

// TestSocketSpawnTrampolineEdgeCases pins the two frame divergences the wire-byte
// review caught, measured against 19f30c46 on a VM. Class A: a resolvable-but-not-
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
	if s.procs.runDir == "" {
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

func goVal(env []string, name string) string {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, name+"="); ok {
			return v
		}
	}
	return ""
}

// TestWrapCmdWithTrampoline proves a resolved command is rewritten to
// `<self> --exec-child <execPath> <origArgv...>` with the Go env held,
// CLAUDE_SSH_RUN_DIR appended, and the exec-error pipe passed as the sole ExtraFile;
// while a command exec.Command could not resolve (cmd.Err set) or an empty run dir is
// left untouched (so its Start error frame is unchanged, and a bare/test socket does
// not trampoline). A missing ABSOLUTE path IS wrapped now — the trampoline reports
// its exec failure over the pipe (see the edge-case socket test).
func TestWrapCmdWithTrampoline(t *testing.T) {
	const runDir = "/x/run/c0ffee01"

	// Resolved command, run dir present -> wrapped, with a live error pipe.
	cmd := exec.Command("/bin/sh", "-c", "true")
	cmd.Env = []string{"GODEBUG=z=1", "PATH=/usr/bin"}
	r, w := wrapCmdWithTrampoline(cmd, runDir)
	if r == nil || w == nil {
		t.Fatal("a resolved command under a run dir must be wrapped (non-nil exec-error pipe)")
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	self := trampolineSelf()
	wantArgs := []string{self, execChildFlag, "/bin/sh", "/bin/sh", "-c", "true"}
	if cmd.Path != self || !slices.Equal(cmd.Args, wantArgs) {
		t.Errorf("wrap args = %v (path %q), want %v (path %q)", cmd.Args, cmd.Path, wantArgs, self)
	}
	if len(cmd.ExtraFiles) != 1 || cmd.ExtraFiles[0] != w {
		t.Errorf("exec-error pipe write end not passed as the sole ExtraFile: %v", cmd.ExtraFiles)
	}
	if slices.Contains(cmd.Env, "GODEBUG=z=1") {
		t.Error("GODEBUG not held")
	}
	if !slices.Contains(cmd.Env, heldEnvPrefix+"GODEBUG=z=1") {
		t.Error("held GODEBUG missing")
	}
	if !slices.Contains(cmd.Env, envRunDir+"="+runDir) {
		t.Errorf("%s not appended: %v", envRunDir, cmd.Env)
	}

	// A bare name exec.Command could not resolve (cmd.Err set) -> untouched, so Start
	// reproduces the exact "exec: … not found in $PATH".
	miss := exec.Command("nonexistent_bare_cmd_xyz")
	if miss.Err == nil {
		t.Skip("exec.Command recorded no lookPath error for a bare missing name")
	}
	missArgs := slices.Clone(miss.Args)
	if mr, mw := wrapCmdWithTrampoline(miss, runDir); mr != nil || mw != nil || !slices.Equal(miss.Args, missArgs) {
		t.Errorf("unresolvable command was wrapped: args=%v pipe=(%v,%v)", miss.Args, mr, mw)
	}

	// No run dir -> untouched even for a resolved command.
	direct := exec.Command("/bin/sh", "-c", "true")
	directArgs := slices.Clone(direct.Args)
	if dr, dw := wrapCmdWithTrampoline(direct, ""); dr != nil || dw != nil || direct.Path != "/bin/sh" || !slices.Equal(direct.Args, directArgs) {
		t.Errorf("empty run dir still wrapped: path=%q args=%v", direct.Path, direct.Args)
	}
}

// TestReadExecChildError proves the parent surfaces a trampoline-reported exec error
// (bytes on the pipe become the spawn error) and treats a clean EOF — the CLOEXEC
// close on a successful exec — as no error.
func TestReadExecChildError(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	const msg = "fork/exec /x: exec format error"
	_, _ = io.WriteString(w, msg)
	_ = w.Close()
	if got := readExecChildError(r); got == nil || got.Error() != msg {
		t.Errorf("readExecChildError(bytes) = %v, want %q", got, msg)
	}

	r2, w2, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = w2.Close() // no bytes -> clean EOF -> success
	if got := readExecChildError(r2); got != nil {
		t.Errorf("readExecChildError(EOF) = %v, want nil", got)
	}
}

// TestChildIdentity proves the CLAUDE_SSH_CHILD value is "<pid>:<startTicks>" with
// this process's real pid and a positive start-time — the pid-reuse-safe identity
// measured in the trampolined child.
func TestChildIdentity(t *testing.T) {
	id := childIdentity()
	if !regexp.MustCompile(`^\d+:\d+$`).MatchString(id) {
		t.Fatalf("childIdentity = %q, want <pid>:<ticks>", id)
	}
	if pid, _, _ := strings.Cut(id, ":"); pid != strconv.Itoa(os.Getpid()) {
		t.Errorf("childIdentity pid = %s, want %d", pid, os.Getpid())
	}
	if ownStartTicks() <= 0 {
		t.Error("ownStartTicks() should be > 0")
	}
}
