package main

import (
	"slices"
	"testing"
)

// The reference daemon's environ contains CLAUDE_SSH_DAEMON_CHILD=1 after the
// self-daemonize re-exec, and that marker is propagated verbatim into every
// process.spawn child (observed in the real spawned agent's /proc/<pid>/environ).
// claustrum sets the same marker on its re-exec ([server.go:142]) and buildEnv
// uses os.Environ() as the base, so it must leak to children too. Pin that
// behavior: a regression that swapped buildEnv's base for an empty []string or
// a curated allow-list would diverge silently — no wire frame mentions the
// marker, but downstream tooling can detect the absence by inspecting its own
// environment.

func TestReplaceOrAppendEnv(t *testing.T) {
	// Existing key is replaced in place.
	got := replaceOrAppendEnv([]string{"A=1", "B=2"}, "A", "9")
	if want := []string{"A=9", "B=2"}; !slices.Equal(got, want) {
		t.Errorf("replace: got %v, want %v", got, want)
	}

	// New key is appended.
	got = replaceOrAppendEnv([]string{"A=1"}, "C", "3")
	if want := []string{"A=1", "C=3"}; !slices.Equal(got, want) {
		t.Errorf("append: got %v, want %v", got, want)
	}

	// Prefix match must be exact ("A=" not a prefix of "AB=").
	got = replaceOrAppendEnv([]string{"AB=2"}, "A", "1")
	if want := []string{"AB=2", "A=1"}; !slices.Equal(got, want) {
		t.Errorf("prefix safety: got %v, want %v", got, want)
	}
}

func TestBuildEnvMergesOverEnviron(t *testing.T) {
	t.Setenv("CLAUSTRUM_TEST_KEEP", "base")
	t.Setenv("CLAUSTRUM_TEST_OVERRIDE", "old")

	env := buildEnv(map[string]string{
		"CLAUSTRUM_TEST_OVERRIDE": "new",
		"CLAUSTRUM_TEST_ADDED":    "added",
	})

	if !slices.Contains(env, "CLAUSTRUM_TEST_KEEP=base") {
		t.Error("inherited environ entry was dropped")
	}
	if !slices.Contains(env, "CLAUSTRUM_TEST_OVERRIDE=new") {
		t.Error("caller override was not applied")
	}
	if slices.Contains(env, "CLAUSTRUM_TEST_OVERRIDE=old") {
		t.Error("stale value survived the override")
	}
	if !slices.Contains(env, "CLAUSTRUM_TEST_ADDED=added") {
		t.Error("new caller key was not appended")
	}
}

func TestSpawnInheritsDaemonChildMarker(t *testing.T) {
	t.Setenv("CLAUDE_SSH_DAEMON_CHILD", "1")
	m := newProcManager()
	t.Cleanup(m.killAll)
	c, frames := pipeConn(t)
	if err := m.spawn(c, "envcheck", "/bin/sh",
		[]string{"-c", `printf %s "$CLAUDE_SSH_DAEMON_CHILD"`}, "", nil); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if got := firstStdout(t, frames); got != "1" {
		t.Errorf("child saw CLAUDE_SSH_DAEMON_CHILD=%q, want %q — daemon re-exec marker must propagate to spawned children for reference parity", got, "1")
	}
}
