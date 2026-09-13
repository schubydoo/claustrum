//go:build linux || darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// The exec-child trampoline (reference build 19f30c46). When the daemon spawns a child
// under a run/<clientId>/ socket, it does not exec the target directly: it re-execs ITSELF
// as `<self> --exec-child <execPath> <argv...>`. The re-exec'd trampoline — running AS the
// child, after fork and before exec — stamps CLAUDE_SSH_CHILD with its own pid and
// start-time (knowable only there), restores the child's held Go-runtime env, then execs
// the real target in place. This gives each spawned child a stable, pid-reuse-safe
// identity in its environment.
//
// Two markers are added to a trampolined child's environment: CLAUDE_SSH_RUN_DIR (set by
// the parent in spawn) and CLAUDE_SSH_CHILD=<pid>:<start> (set here in the child). The five
// Go-runtime vars are stashed under CLAUDE_SSH_HELD_<name> across the re-exec so they do
// not perturb the transient trampoline, then restored.
//
// Because the re-exec'd trampoline is itself a valid binary, exec.Cmd.Start's own
// exec-error pipe reports the trampoline's exec, not the target's. So the trampoline
// carries a second exec-error pipe (fd execErrExtraFD): on a target exec failure it writes
// the exact Go-style "fork/exec <path>: <err>" string and exits, and the parent surfaces
// that as the spawn error frame — byte-identical to a direct exec.Cmd.Start failure; on
// success the pipe is closed by CLOEXEC and the parent reads EOF. So a non-runnable target
// still yields the same -32603 fork/exec frame, never a spurious success.
//
// The logic here is shared across the unix targets; only the child's start-time token is
// OS-specific (ownStartToken: /proc/self/stat clock ticks on linux, the ps start-time on
// darwin). Windows links only a trampoline shim (no re-exec), so there this is a no-op
// (execchild_other.go).
const (
	execChildFlag  = "--exec-child"
	heldEnvPrefix  = "CLAUDE_SSH_HELD_"
	envChildMarker = "CLAUDE_SSH_CHILD"
	envRunDir      = "CLAUDE_SSH_RUN_DIR"
	// execErrExtraFD is the child fd of the trampoline's exec-error pipe. spawn passes the
	// pipe's write end as the sole cmd.ExtraFiles entry, so it lands at fd 3.
	execErrExtraFD = 3
)

// heldGoRuntimeEnv are the Go-runtime env vars held across the re-exec. Holding them keeps
// the target child's settings (e.g. GOMAXPROCS) from changing the trampoline's own Go
// runtime; runExecChild restores them before exec'ing the target.
var heldGoRuntimeEnv = []string{"GODEBUG", "GOGC", "GOMAXPROCS", "GOMEMLIMIT", "GOTRACEBACK"}

// execChildExec is syscall.Exec behind a seam. Real syscall.Exec never returns on success
// (it replaces the process image), so runExecChild's body is only reachable in a re-exec'd
// subprocess, where the parent's coverage cannot see it. A test overrides this to exercise
// runExecChild in-process.
var execChildExec = syscall.Exec

// execChildErrPipe is os.Pipe behind a seam, so a test can force the exec-error pipe
// creation and capture its ends to prove spawn closes them when a later pre-Start step
// fails (the descriptor-leak guard, same idiom as osPipe in process.go).
var execChildErrPipe = os.Pipe

// maybeRunExecChild runs the trampoline when invoked as `<self> --exec-child ...` and never
// returns in that case. It is called at the very top of main, before flag parsing.
func maybeRunExecChild() {
	if len(os.Args) >= 2 && os.Args[1] == execChildFlag {
		runExecChild(os.Args[2:])
	}
}

// runExecChild is the trampoline body. argv is [execPath, childArgv0, childArgv1...]:
// argv[0] is the path to exec (resolved by the parent), argv[1:] is the target's own argv
// (argv[1] is its argv[0]). It marks the exec-error fd CLOEXEC (so a successful exec closes
// it and the parent reads EOF), restores the held Go env, stamps CLAUDE_SSH_CHILD from THIS
// process, then execs the target in place. On exec failure it writes the Go-style
// "fork/exec <path>: <err>" to the exec-error fd so the parent can surface it as the spawn
// error, then exits 127. Never returns on success.
func runExecChild(argv []string) {
	if len(argv) < 2 {
		osExit(127)
	}
	syscall.CloseOnExec(execErrExtraFD)
	env := restoreHeldEnv(os.Environ())
	env = replaceOrAppendEnv(env, envChildMarker, childIdentity())
	err := execChildExec(argv[0], argv[1:], env)
	// Only reached when the target exec failed. Report it exactly as exec.Cmd.Start would
	// (os.PathError{Op:"fork/exec", Path, Err}) so the parent's frame matches a direct
	// spawn, then exit with the shell "cannot exec" code.
	_, _ = syscall.Write(execErrExtraFD, []byte(fmt.Sprintf("fork/exec %s: %s", argv[0], err)))
	osExit(127)
}

// childIdentity is "<pid>:<start>" — this process's pid and its start-time token, the
// pid-reuse-safe identity stamped in CLAUDE_SSH_CHILD. The start token is OS-specific
// (ownStartToken).
func childIdentity() string {
	return strconv.Itoa(os.Getpid()) + ":" + ownStartToken()
}

// wrapCmdWithTrampoline rewrites cmd to launch through the exec-child trampoline and
// returns the exec-error pipe's read end (nil when not wrapped). It wraps only when a run
// dir is known (the socket is run/<clientId>/ shaped) and the command already resolved — a
// bare name exec.Command could not find leaves cmd.Err set, and is left untouched so
// cmd.Start reproduces the exact "exec: … not found in $PATH". cmd.Path (resolved by
// exec.Command) is passed as the exec path and the original cmd.Args (argv[0] = the
// caller's command) is preserved, exactly what a direct Start runs; a relative path
// resolves in the child against cmd.Dir, which the trampoline inherits. Any other exec
// failure (missing absolute path, non-executable format, permission) is reported by the
// trampoline over the returned pipe, not swallowed. It returns the pipe's read AND write
// ends (both nil when not wrapped): after Start the caller closes the write end (its copy)
// and reads the read end. See readExecChildError.
func wrapCmdWithTrampoline(cmd *exec.Cmd, runDir string) (execErrR, execErrW *os.File) {
	if runDir == "" || cmd.Err != nil {
		return nil, nil
	}
	self := trampolineSelf()
	if self == "" {
		// The daemon's own binary is not re-executable (e.g. it was uninstalled while the
		// daemon kept running; darwin cannot re-exec a path whose file is gone). Spawn the
		// target DIRECTLY, but still tag it with the run dir. This matches the reference: after
		// an uninstall its children keep CLAUDE_SSH_RUN_DIR and just lack the CLAUDE_SSH_CHILD
		// marker the re-exec would have added, rather than failing to spawn. On linux
		// trampolineSelf is /proc/self/exe, which survives an unlink, so this path is not taken.
		cmd.Env = replaceOrAppendEnv(cmd.Env, envRunDir, runDir)
		return nil, nil
	}
	r, w, err := execChildErrPipe()
	if err != nil {
		return nil, nil // fall back to a direct spawn; the child just lacks the markers
	}
	cmd.ExtraFiles = append(cmd.ExtraFiles, w) // → execErrExtraFD in the child
	cmd.Args = append([]string{self, execChildFlag, cmd.Path}, cmd.Args...)
	cmd.Path = self
	cmd.Env = holdGoEnv(cmd.Env)
	// Authoritative: the daemon's run dir must win, so replace any existing
	// CLAUDE_SSH_RUN_DIR (caller-supplied or inherited) rather than appending a second entry
	// a getenv could resolve to the stale value — same as the CLAUDE_SSH_CHILD marker below.
	cmd.Env = replaceOrAppendEnv(cmd.Env, envRunDir, runDir)
	return r, w
}

// holdGoEnv stashes each Go-runtime var present in env under CLAUDE_SSH_HELD_<name> and
// removes the bare var, so the re-exec'd trampoline starts with default Go runtime
// settings. runExecChild restores them before exec'ing the target.
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

// restoreHeldEnv is the inverse of holdGoEnv: for each of the SAME Go-runtime vars holdGoEnv
// stashes, CLAUDE_SSH_HELD_<name>=<val> is un-prefixed back to <name>=<val> (overriding any
// bare <name>) and the held entry is dropped. It recognizes ONLY those known names,
// symmetric with holdGoEnv — a stray CLAUDE_SSH_HELD_<other> (one a process.spawn caller put
// in the env) is not a held var, so it is passed through untouched rather than un-prefixed,
// and cannot smuggle an arbitrary <other>=<val> into the target.
func restoreHeldEnv(env []string) []string {
	heldKey := make(map[string]string, len(heldGoRuntimeEnv)) // "CLAUDE_SSH_HELD_<name>" -> "<name>"
	for _, name := range heldGoRuntimeEnv {
		heldKey[heldEnvPrefix+name] = name
	}
	out := make([]string, 0, len(env))
	var held [][2]string
	for _, e := range env {
		if i := strings.IndexByte(e, '='); i > 0 {
			if name, ok := heldKey[e[:i]]; ok {
				held = append(held, [2]string{name, e[i+1:]})
				continue
			}
		}
		out = append(out, e)
	}
	for _, h := range held {
		out = replaceOrAppendEnv(out, h[0], h[1])
	}
	return out
}

// trampolineSelf is the path the daemon re-execs as the trampoline, or "" when it has no
// re-executable self (then wrapCmdWithTrampoline spawns the target directly). It prefers
// /proc/self/exe, which points at the running image even after the on-disk binary is
// unlinked or replaced (linux). On darwin there is no /proc/self/exe, so os.Executable is
// used — but only when its file still exists: a daemon whose binary was uninstalled while
// running cannot re-exec it, and returning "" makes the spawn fall back to a direct launch,
// matching the reference. A seam so a test can force the "no self" fallback.
var trampolineSelf = func() string {
	const procSelfExe = "/proc/self/exe"
	if _, err := os.Stat(procSelfExe); err == nil {
		return procSelfExe
	}
	if exe, err := os.Executable(); err == nil {
		if _, err := os.Stat(exe); err == nil {
			return exe
		}
	}
	return ""
}
