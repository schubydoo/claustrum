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

// The exec-child trampoline (reference build 19f30c46). When the daemon spawns a child,
// on every socket shape, it does not exec the target directly: it re-execs ITSELF
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
// darwin). On windows this is a no-op (execchild_other.go): the reference stamps no marker
// there (VM-measured, a windows child's env matches a bare-socket child's), so the daemon
// spawns the target directly.
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
	// The parent removed every CLAUDE_SSH_CHILD entry (runDirEnv), so this adds the
	// entry at the end. It comes right after CLAUDE_SSH_RUN_DIR: restoreHeldEnv puts a
	// held Go runtime variable back at its own place, before that entry.
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
// dir is known (the daemon has a socket path) and the command already resolved. A
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
		cmd.Env = runDirEnv(cmd.Env, runDir)
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
	cmd.Env = runDirEnv(cmd.Env, runDir)
	return r, w
}

// runDirEnv returns env with the run dir entry of the daemon as its last entry. It
// first removes every CLAUDE_SSH_RUN_DIR and CLAUDE_SSH_CHILD entry, so a value from
// the caller or from the daemon environment never reaches the child and holds no
// earlier place. The trampoline then adds CLAUDE_SSH_CHILD after it.
//
// Measured on Linux and macOS against 89cb6289. The child environment ends with
// CLAUDE_SSH_RUN_DIR and then CLAUDE_SSH_CHILD (rows F1 to F5). A caller value of
// either one does not reach the child (rows EV02a, EV02e). A copy in the daemon
// environment does not keep its place (rows EV03a, EV03e).
//
// An environment that holds one of the five Go runtime variables ends with the same
// two entries (Linux rows P4a to P4c, GOGC and GOMAXPROCS). holdGoEnv and
// restoreHeldEnv keep such a variable at its own place, before this entry.
func runDirEnv(env []string, runDir string) []string {
	env = removeEnvKey(removeEnvKey(env, envRunDir), envChildMarker)
	return append(env, envRunDir+"="+runDir)
}

// trampolineFallbackEnv is the environment of a command that spawn starts directly
// after the start of the trampoline failed. env is the environment before the wrap.
// The command gets the run dir entry, and no CLAUDE_SSH_CHILD entry, because only the
// trampoline can stamp that one. This is the same environment as the direct start in
// wrapCmdWithTrampoline for a daemon with no re-executable binary. It is claustrum's
// choice and is not measured: the one measured row (CW03, Linux) has a direct start
// that fails too.
func trampolineFallbackEnv(env []string, runDir string) []string {
	return runDirEnv(env, runDir)
}

// holdGoEnv renames each Go-runtime var present in env to CLAUDE_SSH_HELD_<name>, at
// the place of the var, so the re-exec'd trampoline starts with default Go runtime
// settings. runExecChild restores them before exec'ing the target.
//
// The held entry keeps the place of the variable, so the child gets the variable
// where it was. Measured on Linux against 89cb6289 (rows P4a to P4c): a GOGC of the
// daemon environment stays at its place there, before CLAUDE_SSH_DAEMON_CHILD. A
// GOMAXPROCS of the spawn env stands where a new caller key stands, before
// CLAUDE_SSH_RUN_DIR. The other three names are not measured. They get the same rule.
func holdGoEnv(env []string) []string {
	for _, name := range heldGoRuntimeEnv {
		prefix := name + "="
		for i, e := range env {
			if strings.HasPrefix(e, prefix) {
				env[i] = heldEnvPrefix + e
				break
			}
		}
	}
	return env
}

// restoreHeldEnv is the inverse of holdGoEnv: for each of the SAME Go-runtime vars holdGoEnv
// stashes, CLAUDE_SSH_HELD_<name>=<val> is un-prefixed back to <name>=<val> at the place
// of the held entry, and a bare <name> entry is dropped. It recognizes ONLY those known names,
// symmetric with holdGoEnv — a stray CLAUDE_SSH_HELD_<other> (one a process.spawn caller put
// in the env) is not a held var, so it is passed through untouched rather than un-prefixed,
// and cannot smuggle an arbitrary <other>=<val> into the target.
func restoreHeldEnv(env []string) []string {
	// held is the set of names that env holds: such a name has a CLAUDE_SSH_HELD_ entry.
	held := make(map[string]bool, len(heldGoRuntimeEnv))
	for _, e := range env {
		for _, name := range heldGoRuntimeEnv {
			if strings.HasPrefix(e, heldEnvPrefix+name+"=") {
				held[name] = true
			}
		}
	}
	out := make([]string, 0, len(env))
	at := make(map[string]int, len(held)) // the place in out of each restored name
	for _, e := range env {
		key, _, _ := strings.Cut(e, "=")
		name, isHeld := strings.CutPrefix(key, heldEnvPrefix)
		switch {
		case isHeld && held[name]:
			restored := strings.TrimPrefix(e, heldEnvPrefix)
			if i, again := at[name]; again {
				out[i] = restored // a second held entry of one name: the last value wins
				continue
			}
			at[name] = len(out)
			out = append(out, restored)
		case !held[key]:
			out = append(out, e)
		}
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
