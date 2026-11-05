//go:build linux

package supervisor

import (
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/schubydoo/claustrum/internal/childrec"
)

// TestMaybeRunExecChildExecsTarget exercises the trampoline body in-process by
// seaming syscall.Exec and os.Exit (the real Exec never returns, so this path is
// otherwise only reachable in a re-exec'd subprocess). It confirms MaybeRunExecChild
// dispatches on the --exec-child arg, execs the resolved path with the target's own
// argv, stamps CLAUDE_SSH_CHILD, restores a held Go var (overriding the bare one),
// and leaves no CLAUDE_SSH_HELD_ residue.
func TestMaybeRunExecChildExecsTarget(t *testing.T) {
	var gotPath string
	var gotArgv, gotEnv []string
	oldExec, oldExit := execChildExec, execChildExit
	execChildExec = func(path string, argv, env []string) error {
		gotPath, gotArgv, gotEnv = path, argv, env
		return nil
	}
	execChildExit = func(int) {}
	t.Cleanup(func() { execChildExec, execChildExit = oldExec, oldExit })

	oldArgs := os.Args
	os.Args = []string{"/self", execChildFlag, "/bin/echo", "echo", "hi"}
	t.Cleanup(func() { os.Args = oldArgs })
	t.Setenv("GODEBUG", "orig")
	t.Setenv(HeldEnvPrefix+"GODEBUG", "held=1")

	MaybeRunExecChild()

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
		if strings.HasPrefix(e, HeldEnvPrefix) {
			t.Errorf("HELD residue in exec env: %q", e)
		}
	}
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
	r, w := WrapCmdWithTrampoline(cmd, runDir)
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
	if !slices.Contains(cmd.Env, HeldEnvPrefix+"GODEBUG=z=1") {
		t.Error("held GODEBUG missing")
	}
	if !slices.Contains(cmd.Env, EnvRunDir+"="+runDir) {
		t.Errorf("%s not set: %v", EnvRunDir, cmd.Env)
	}

	// A pre-existing CLAUDE_SSH_RUN_DIR (caller-supplied or inherited) is REPLACED with
	// the daemon's authoritative run dir, not duplicated — else a getenv could resolve
	// the child's run dir to the stale value (greptile P1).
	dup := exec.Command("/bin/sh", "-c", "true")
	dup.Env = []string{EnvRunDir + "=/caller/bogus", "PATH=/usr/bin"}
	dr, dw := WrapCmdWithTrampoline(dup, runDir)
	if dr == nil || dw == nil {
		t.Fatal("a resolved command under a run dir must be wrapped")
	}
	t.Cleanup(func() { _ = dr.Close(); _ = dw.Close() })
	n := 0
	for _, e := range dup.Env {
		if strings.HasPrefix(e, EnvRunDir+"=") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%s appears %d times, want exactly 1 (authoritative, replaced): %v", EnvRunDir, n, dup.Env)
	}
	if !slices.Contains(dup.Env, EnvRunDir+"="+runDir) || slices.Contains(dup.Env, EnvRunDir+"=/caller/bogus") {
		t.Errorf("%s not replaced with the daemon's run dir: %v", EnvRunDir, dup.Env)
	}

	// A bare name exec.Command could not resolve (cmd.Err set) -> untouched, so Start
	// reproduces the exact "exec: … not found in $PATH".
	miss := exec.Command("nonexistent_bare_cmd_xyz")
	if miss.Err == nil {
		t.Skip("exec.Command recorded no lookPath error for a bare missing name")
	}
	missArgs := slices.Clone(miss.Args)
	if mr, mw := WrapCmdWithTrampoline(miss, runDir); mr != nil || mw != nil || !slices.Equal(miss.Args, missArgs) {
		t.Errorf("unresolvable command was wrapped: args=%v pipe=(%v,%v)", miss.Args, mr, mw)
	}

	// No run dir -> untouched even for a resolved command.
	direct := exec.Command("/bin/sh", "-c", "true")
	directArgs := slices.Clone(direct.Args)
	if dr, dw := WrapCmdWithTrampoline(direct, ""); dr != nil || dw != nil || direct.Path != "/bin/sh" || !slices.Equal(direct.Args, directArgs) {
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
	if got := ReadExecChildError(r); got == nil || got.Error() != msg {
		t.Errorf("ReadExecChildError(bytes) = %v, want %q", got, msg)
	}

	r2, w2, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = w2.Close() // no bytes -> clean EOF -> success
	if got := ReadExecChildError(r2); got != nil {
		t.Errorf("ReadExecChildError(EOF) = %v, want nil", got)
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
	if childrec.OwnStartTicks() <= 0 {
		t.Error("childrec.OwnStartTicks() should be > 0")
	}
}
