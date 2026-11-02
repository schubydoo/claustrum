package shellenv

import (
	"testing"
	"time"
)

// TestResetForTestPutsBackTheClockAndTheGate holds the hooks that the tests of
// package main use to fake the PATH read.
func TestResetForTestPutsBackTheClockAndTheGate(t *testing.T) {
	t.Cleanup(ResetForTest)
	ResetForTest()
	fixed := time.Unix(1_000_000, 0)
	UseClockForTest(func() time.Time { return fixed })
	ArmForTest()
	loginPATHs.mu.Lock()
	armed, gate, now := loginPATHs.armed, loginPATHs.gate, loginPATHs.clock()
	loginPATHs.mu.Unlock()
	if !armed || gate == loginShellGate || !now.Equal(fixed) {
		t.Fatalf("after the hooks: armed %v, own gate %v, clock %v", armed, gate != loginShellGate, now)
	}
	ResetForTest()
	loginPATHs.mu.Lock()
	armed, gate, now = loginPATHs.armed, loginPATHs.gate, loginPATHs.clock()
	loginPATHs.mu.Unlock()
	if armed || gate != loginShellGate || now.Equal(fixed) {
		t.Errorf("after the reset: armed %v, production gate %v, clock %v", armed, gate == loginShellGate, now)
	}
}

// TestRunningReadForTestGivesTheEndOfTheRead: no channel with no read, and the
// channel of the read that runs, closed at its result.
func TestRunningReadForTestGivesTheEndOfTheRead(t *testing.T) {
	f := installPathReadFake(t)
	if end := RunningReadForTest(); end != nil {
		t.Fatal("a channel with no read that runs")
	}
	WarmAtStart()
	r := startedRead(t, f)
	end := RunningReadForTest()
	if end != r.end {
		t.Fatal("the channel is not the end of the read that runs")
	}
	f.answer <- pathAnswer{path: "/login/bin"}
	select {
	case <-end:
	case <-time.After(10 * time.Second):
		t.Fatal("the channel did not close at the result")
	}
}

// TestSetSpawnWaitForTestRestores: the returned func puts the old wait back.
func TestSetSpawnWaitForTestRestores(t *testing.T) {
	old := loginPATHSpawnWait
	restore := SetSpawnWaitForTest(time.Millisecond)
	got := loginPATHSpawnWait
	restore()
	if got != time.Millisecond || loginPATHSpawnWait != old {
		t.Errorf("wait = %v during, %v after; want 1ms, then %v", got, loginPATHSpawnWait, old)
	}
}

// TestSpawnHorizonForTest gives the horizon of the spawn wait.
func TestSpawnHorizonForTest(t *testing.T) {
	if got := SpawnHorizonForTest(); got != loginPATHSpawnHorizon {
		t.Errorf("SpawnHorizonForTest() = %v, want %v", got, loginPATHSpawnHorizon)
	}
}
