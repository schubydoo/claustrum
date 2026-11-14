package supervisor

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestGivenUpIDRefusesSpawn: an id that no process had is given up, and its spawn
// gets the refusal of 4c111e86 with no child and the failed-start line. Ids of 128
// bytes are given up, 129 bytes are not. An id that a process has is not given up
// (measured on Linux and Windows). Mutations: drop the given-up test in spawnWith,
// give up ids of any length, give up an id that a process has.
func TestGivenUpIDRefusesSpawn(t *testing.T) {
	buf := captureLogBuf(t)
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	exe, env := helperCommand(t, "sleep")
	c, _ := pipeConn(t)
	id128, id129 := strings.Repeat("L", 128), strings.Repeat("L", 129)
	for _, id := range []string{"ghost1", id128, id129} {
		m.GiveUpUnknown(id)
	}
	for _, id := range []string{"ghost1", id128} {
		if _, err := m.spawnVia(c, id, exe, []string{"60"}, "", env, false, nil, 0); err != errGivenUpID {
			t.Errorf("spawn of given-up id %.8s… = %v, want the given-up refusal", id, err)
		}
		if m.Get(id) != nil {
			t.Errorf("a refused spawn of %.8s… left a record", id)
		}
	}
	if !strings.Contains(buf.String(), "[process.Manager] Failed to start process ghost1: this id was asked after, or asked to be ended, while no process had it: take a fresh one\n") {
		t.Errorf("no failed-start line in:\n%s", buf.String())
	}
	if _, err := m.spawnVia(c, id129, exe, []string{"60"}, "", env, false, nil, 0); err != nil {
		t.Errorf("spawn of a 129-byte id = %v, want a start", err)
	}
	if _, err := m.spawnVia(c, "live1", exe, []string{"1"}, "", env, false, nil, 0); err != nil {
		t.Fatal(err)
	}
	m.GiveUpUnknown("live1")
	if _, err := m.spawnVia(c, "live1", exe, []string{"1"}, "", env, false, nil, 0); err != nil {
		t.Errorf("spawn of an id that a process had = %v, want a start", err)
	}
}

// TestGivenUpIDWindow: a given-up id stays refused until givenUpAge passed since it
// was given up, and a refused spawn does not renew the time (measured on Windows:
// refused at 840 s, started at 910 s). The time is moved back in place of a wait.
// Mutations: renew the time at a refused spawn, drop pruneGivenUpLocked.
func TestGivenUpIDWindow(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	exe, env := helperCommand(t, "sleep")
	c, _ := pipeConn(t)
	m.GiveUpUnknown("slow-a")
	m.mu.Lock()
	at := time.Now().Add(-m.givenUpAge + time.Minute)
	m.givenUp["slow-a"] = at
	m.mu.Unlock()
	if _, err := m.spawnVia(c, "slow-a", exe, []string{"60"}, "", env, false, nil, 0); err != errGivenUpID {
		t.Fatalf("spawn one minute before the end = %v, want the given-up refusal", err)
	}
	m.mu.Lock()
	kept := m.givenUp["slow-a"]
	m.givenUp["slow-a"] = kept.Add(-time.Minute)
	m.mu.Unlock()
	if !kept.Equal(at) {
		t.Errorf("the refused spawn moved the time from %v to %v", at, kept)
	}
	if _, err := m.spawnVia(c, "slow-a", exe, []string{"60"}, "", env, false, nil, 0); err != nil {
		t.Errorf("spawn after the end = %v, want a start", err)
	}
}

// TestGivenUpIDSkipsLinkBoundStart: a give-up of an id whose link-bound spawn is
// between its id test and its table entry gives nothing up (claustrum's own, not
// measured). The gitRead step holds the spawn in that window, gives the id up there
// and then fails the spawn, so no child starts. Mutation: drop the linkStarting
// test in GiveUpUnknown.
func TestGivenUpIDSkipsLinkBoundStart(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	exe, _ := helperCommand(t, "sleep")
	c, _ := pipeConn(t)
	stop := errors.New("held start ends here")
	step := func(env []string) ([]string, []string, func(), error) {
		m.GiveUpUnknown("linked1")
		return nil, nil, nil, stop
	}
	_, err := m.spawnWith(c, "linked1", exe, nil, "", nil, false, nil, time.Minute, TempRootAsk{}, nil, &gitReadStart{step: step}, nil)
	if !errors.Is(err, stop) {
		t.Fatalf("spawn = %v, want the error of the step", err)
	}
	m.mu.Lock()
	given := m.givenUpLocked("linked1")
	m.mu.Unlock()
	if given {
		t.Error("a give-up during a link-bound start gave the id up")
	}
}
