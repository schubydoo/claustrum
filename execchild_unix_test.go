//go:build linux || darwin

package main

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// TestWrapCmdFallsBackWhenNoSelf covers the no-re-executable-self path: when trampolineSelf
// returns "" (the daemon's own binary is gone, e.g. uninstalled while running), the spawn
// must fall back to launching the target directly — no re-exec, no exec-error pipe — while
// still tagging the child with CLAUDE_SSH_RUN_DIR. This matches the reference, whose
// children after an uninstall keep the run-dir marker and only lose CLAUDE_SSH_CHILD. On
// linux trampolineSelf is /proc/self/exe (survives an unlink), so the real trigger is
// darwin; the seam exercises the shared decision here.
func TestWrapCmdFallsBackWhenNoSelf(t *testing.T) {
	old := trampolineSelf
	t.Cleanup(func() { trampolineSelf = old })
	trampolineSelf = func() string { return "" }

	cmd := exec.Command("/bin/echo", "hi")
	// Two stale entries: the run dir entry goes to the end and the child marker goes.
	cmd.Env = []string{"CLAUDE_SSH_RUN_DIR=/stale", "PATH=/usr/bin", "CLAUDE_SSH_CHILD=1:1"}
	r, w := wrapCmdWithTrampoline(cmd, "/opt/claude/run/c0ffee01")

	if r != nil || w != nil {
		t.Error("no-self fallback returned an exec-error pipe; want nil,nil (direct spawn)")
	}
	if cmd.Path != "/bin/echo" || len(cmd.Args) != 2 || cmd.Args[0] != "/bin/echo" {
		t.Errorf("cmd was rewritten to re-exec (path=%q args=%v); want an unchanged direct spawn", cmd.Path, cmd.Args)
	}
	if len(cmd.ExtraFiles) != 0 {
		t.Error("no-self fallback attached an extra fd; want none")
	}
	if want := []string{"PATH=/usr/bin", "CLAUDE_SSH_RUN_DIR=/opt/claude/run/c0ffee01"}; !slices.Equal(cmd.Env, want) {
		t.Errorf("env of the direct-spawn fallback = %v, want %v", cmd.Env, want)
	}
}

// TestHoldRestoreGoEnvRoundTrip proves holdGoEnv → restoreHeldEnv is the identity for
// the five Go-runtime vars (the child ends up with the daemon's values restored),
// that non-held vars pass through untouched, and that no CLAUDE_SSH_HELD_ residue
// survives — matching the measured child env, where GODEBUG round-tripped and no
// held key remained. Each variable keeps its place (89cb6289, Linux rows P4a to P4c).
func TestHoldRestoreGoEnvRoundTrip(t *testing.T) {
	in := []string{
		"GODEBUG=x=1,y=2", "PATH=/usr/bin", "GOGC=50", "GOMAXPROCS=4", "FOO=bar",
		"GOMEMLIMIT=123MiB", "CLAUDE_SSH_RUN_DIR=/x/run/c0ffee01", "GOTRACEBACK=all",
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
	if !slices.Equal(got, in) {
		t.Errorf("round-trip mismatch:\n got=%v\nwant=%v", got, in)
	}
}

// TestRestoreHeldEnvIgnoresStrayPrefix proves restore is symmetric with holdGoEnv: it
// un-prefixes ONLY the known Go-runtime held vars, so a CLAUDE_SSH_HELD_<other> a
// process.spawn caller put in the env is passed through untouched and cannot smuggle
// an arbitrary <other>=<val> into the target, nor override the target's own <other>
// (greptile P1). The real held Go var is still restored, and its held entry consumed.
func TestRestoreHeldEnvIgnoresStrayPrefix(t *testing.T) {
	in := []string{
		heldEnvPrefix + "GODEBUG=x=1", // a real held Go var -> restored to GODEBUG=x=1
		heldEnvPrefix + "FOO=evil",    // NOT a held var -> must NOT become FOO=evil
		"FOO=legit",
		"PATH=/usr/bin",
	}
	got := restoreHeldEnv(in)
	if !slices.Contains(got, "GODEBUG=x=1") {
		t.Errorf("held Go var GODEBUG not restored: %v", got)
	}
	if slices.Contains(got, "FOO=evil") {
		t.Errorf("stray %sFOO was un-prefixed into FOO=evil (env injection): %v", heldEnvPrefix, got)
	}
	if !slices.Contains(got, "FOO=legit") {
		t.Errorf("caller's own FOO=legit must survive, not be overridden: %v", got)
	}
	if !slices.Contains(got, heldEnvPrefix+"FOO=evil") {
		t.Errorf("stray held-prefix entry should pass through untouched: %v", got)
	}
	if slices.Contains(got, heldEnvPrefix+"GODEBUG=x=1") {
		t.Errorf("recognized held entry should be consumed, not left as residue: %v", got)
	}
}

// TestRestoreHeldEnvLastHeldValueWins pins the rule for two held entries of one name:
// the last value wins, at the place of the first. A spawn caller can send
// CLAUDE_SSH_HELD_GOGC next to a GOGC that the wrap holds. No row measures this. It is
// claustrum's own choice.
func TestRestoreHeldEnvLastHeldValueWins(t *testing.T) {
	got := restoreHeldEnv([]string{heldEnvPrefix + "GOGC=1", "X=y", heldEnvPrefix + "GOGC=2"})
	if want := []string{"GOGC=2", "X=y"}; !slices.Equal(got, want) {
		t.Errorf("restoreHeldEnv = %v, want %v", got, want)
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
