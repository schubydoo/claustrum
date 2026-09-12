//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// The exec-child trampoline (reference build 19f30c46, linux). When the daemon
// spawns a child under a run/<clientId>/ socket, it does not exec the target
// directly: it re-execs ITSELF as `<self> --exec-child <execPath> <argv...>`. The
// re-exec'd trampoline — running AS the child, after fork and before exec — stamps
// CLAUDE_SSH_CHILD with its own pid and start-time (knowable only there), restores
// the child's held Go-runtime env, then execs the real target in place. This gives
// each spawned child a stable, pid-reuse-safe identity in its environment.
//
// Two markers are added to a trampolined child's environment: CLAUDE_SSH_RUN_DIR
// (set by the parent in spawn) and CLAUDE_SSH_CHILD=<pid>:<startTicks> (set here in
// the child). The five Go-runtime vars are stashed under CLAUDE_SSH_HELD_<name>
// across the re-exec so they do not perturb the transient trampoline, then restored.
//
// Because the re-exec'd trampoline is itself a valid binary, exec.Cmd.Start's own
// exec-error pipe reports the trampoline's exec, not the target's. So the trampoline
// carries a second exec-error pipe (fd execErrExtraFD): on a target exec failure it
// writes the exact Go-style "fork/exec <path>: <err>" string and exits, and the
// parent surfaces that as the spawn error frame — byte-identical to a direct
// exec.Cmd.Start failure; on success the pipe is closed by CLOEXEC and the parent
// reads EOF. So a non-runnable target still yields the same -32603 fork/exec frame,
// never a spurious success. Verified against 19f30c46 on a VM for a valid command, a
// missing command, a non-executable-format +x file, and a relative command under a
// cwd. Darwin links the same subsystem with a different (inferred) start-time source,
// and Windows links a trampoline shim whose behavior is not yet analyzed; both are
// build-tagged out here and reconciled in follow-up slices.
const (
	execChildFlag  = "--exec-child"
	heldEnvPrefix  = "CLAUDE_SSH_HELD_"
	envChildMarker = "CLAUDE_SSH_CHILD"
	envRunDir      = "CLAUDE_SSH_RUN_DIR"
	// execErrExtraFD is the child fd of the trampoline's exec-error pipe. spawn passes
	// the pipe's write end as the sole cmd.ExtraFiles entry, so it lands at fd 3.
	execErrExtraFD = 3
)

// heldGoRuntimeEnv are the Go-runtime env vars held across the re-exec. Holding them
// keeps the target child's settings (e.g. GOMAXPROCS) from changing the trampoline's
// own Go runtime; runExecChild restores them before exec'ing the target.
var heldGoRuntimeEnv = []string{"GODEBUG", "GOGC", "GOMAXPROCS", "GOMEMLIMIT", "GOTRACEBACK"}

// execChildExec is syscall.Exec behind a seam. Real syscall.Exec never returns on
// success (it replaces the process image), so runExecChild's body is only reachable
// in a re-exec'd subprocess, where the parent's coverage cannot see it. A test
// overrides this to exercise runExecChild in-process.
var execChildExec = syscall.Exec

// maybeRunExecChild runs the trampoline when invoked as `<self> --exec-child ...`
// and never returns in that case. It is called at the very top of main, before flag
// parsing, matching the reference (which intercepts the exact "--exec-child" arg
// before its flag set sees it — the arg is not a registered flag).
func maybeRunExecChild() {
	if len(os.Args) >= 2 && os.Args[1] == execChildFlag {
		runExecChild(os.Args[2:])
	}
}

// runExecChild is the trampoline body. argv is [execPath, childArgv0, childArgv1...]:
// argv[0] is the path to exec (resolved by the parent), argv[1:] is the target's own
// argv (argv[1] is its argv[0]). It marks the exec-error fd CLOEXEC (so a successful
// exec closes it and the parent reads EOF), restores the held Go env, stamps
// CLAUDE_SSH_CHILD from THIS process, then execs the target in place. On exec failure
// it writes the Go-style "fork/exec <path>: <err>" to the exec-error fd so the parent
// can surface it as the spawn error, then exits 127. Never returns on success.
func runExecChild(argv []string) {
	if len(argv) < 2 {
		osExit(127)
	}
	syscall.CloseOnExec(execErrExtraFD)
	env := restoreHeldEnv(os.Environ())
	env = replaceOrAppendEnv(env, envChildMarker, childIdentity())
	err := execChildExec(argv[0], argv[1:], env)
	// Only reached when the target exec failed. Report it exactly as exec.Cmd.Start
	// would (os.PathError{Op:"fork/exec", Path, Err}) so the parent's frame matches a
	// direct spawn, then exit with the shell "cannot exec" code.
	_, _ = syscall.Write(execErrExtraFD, []byte(fmt.Sprintf("fork/exec %s: %s", argv[0], err)))
	osExit(127)
}

// childIdentity is "<pid>:<startTicks>" — this process's pid and its start-time in
// clock ticks since boot, the pid-reuse-safe identity stamped in CLAUDE_SSH_CHILD.
func childIdentity() string {
	return strconv.Itoa(os.Getpid()) + ":" + strconv.FormatInt(ownStartTicks(), 10)
}

// wrapCmdWithTrampoline rewrites cmd to launch through the exec-child trampoline and
// returns the exec-error pipe's read end (nil when not wrapped). It wraps only when a
// run dir is known (the socket is run/<clientId>/ shaped) and the command already
// resolved — a bare name exec.Command could not find leaves cmd.Err set, and is left
// untouched so cmd.Start reproduces the exact "exec: … not found in $PATH". cmd.Path
// (resolved by exec.Command) is passed as the exec path and the original cmd.Args
// (argv[0] = the caller's command) is preserved, exactly what a direct Start runs; a
// relative path resolves in the child against cmd.Dir, which the trampoline inherits.
// Any other exec failure (missing absolute path, non-executable format, permission)
// is reported by the trampoline over the returned pipe, not swallowed. It returns the
// pipe's read AND write ends (both nil when not wrapped): after Start the caller
// closes the write end (its copy) and reads the read end. See readExecChildError.
func wrapCmdWithTrampoline(cmd *exec.Cmd, runDir string) (execErrR, execErrW *os.File) {
	if runDir == "" || cmd.Err != nil {
		return nil, nil
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil // fall back to a direct spawn; the child just lacks the markers
	}
	cmd.ExtraFiles = append(cmd.ExtraFiles, w) // → execErrExtraFD in the child
	self := trampolineSelf()
	cmd.Args = append([]string{self, execChildFlag, cmd.Path}, cmd.Args...)
	cmd.Path = self
	cmd.Env = holdGoEnv(cmd.Env)
	cmd.Env = append(cmd.Env, envRunDir+"="+runDir)
	return r, w
}

// holdGoEnv stashes each Go-runtime var present in env under CLAUDE_SSH_HELD_<name>
// and removes the bare var, so the re-exec'd trampoline starts with default Go
// runtime settings. runExecChild restores them before exec'ing the target.
func holdGoEnv(env []string) []string {
	for _, name := range heldGoRuntimeEnv {
		prefix := name + "="
		for i, e := range env {
			if strings.HasPrefix(e, prefix) {
				env = append(env, heldEnvPrefix+e)
				env = append(env[:i], env[i+1:]...)
				break
			}
		}
	}
	return env
}

// restoreHeldEnv is the inverse of holdGoEnv: every CLAUDE_SSH_HELD_<name>=<val> is
// un-prefixed back to <name>=<val> (overriding any bare <name>), and the held
// entries are dropped.
func restoreHeldEnv(env []string) []string {
	out := make([]string, 0, len(env))
	var held [][2]string
	for _, e := range env {
		if strings.HasPrefix(e, heldEnvPrefix) {
			if kv := e[len(heldEnvPrefix):]; strings.IndexByte(kv, '=') > 0 {
				i := strings.IndexByte(kv, '=')
				held = append(held, [2]string{kv[:i], kv[i+1:]})
			}
			continue
		}
		out = append(out, e)
	}
	for _, h := range held {
		out = replaceOrAppendEnv(out, h[0], h[1])
	}
	return out
}

// trampolineSelf is the path the daemon re-execs as the trampoline: /proc/self/exe
// when available (so a binary replaced in place still re-execs the running image),
// else os.Executable.
func trampolineSelf() string {
	const procSelfExe = "/proc/self/exe"
	if _, err := os.Stat(procSelfExe); err == nil {
		return procSelfExe
	}
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return procSelfExe
}

// ownStartTicks returns this process's start-time in clock ticks since boot — field
// 22 of /proc/self/stat, the value the reference embeds in CLAUDE_SSH_CHILD. Field 2
// (comm) is parenthesized and may contain spaces or ')', so parsing resumes after
// the last ')': the remaining fields start at 3 (state), so starttime (field 22) is
// index 22-3 = 19.
func ownStartTicks() int64 {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0
	}
	fields := strings.Fields(s[i+1:])
	if len(fields) <= 19 {
		return 0
	}
	v, _ := strconv.ParseInt(fields[19], 10, 64)
	return v
}
