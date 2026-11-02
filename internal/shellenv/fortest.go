package shellenv

import "time"

// The functions below let the tests of package main drive the PATH read. Only
// tests call them.

// ResetForTest ends a read that runs, with no result line, and clears the state of
// the PATH read. The clock and the gate go back to production.
func ResetForTest() {
	s := loginPATHs
	s.mu.Lock()
	r := s.run
	if r != nil {
		r.quiet = true
	}
	s.mu.Unlock()
	if r != nil {
		r.cancel()
		<-r.end
	}
	s.mu.Lock()
	s.armed, s.closed, s.good, s.path = false, false, false, ""
	s.failures, s.nextTry, s.run = 0, time.Time{}, nil
	s.clock, s.gate = time.Now, loginShellGate
	s.mu.Unlock()
}

// ArmForTest lets the reads of the login-shell PATH run. It starts no read.
func ArmForTest() { armLoginPATH() }

// UseClockForTest gives the PATH read the clock and a gate of its own.
func UseClockForTest(clock func() time.Time) {
	loginPATHs.mu.Lock()
	loginPATHs.clock, loginPATHs.gate = clock, make(chan struct{}, 1)
	loginPATHs.mu.Unlock()
}

// RunningReadForTest returns a channel that is closed when the read that runs
// has its result, or nil when no read runs.
func RunningReadForTest() <-chan struct{} {
	loginPATHs.mu.Lock()
	defer loginPATHs.mu.Unlock()
	if loginPATHs.run == nil {
		return nil
	}
	return loginPATHs.run.end
}

// SetSpawnWaitForTest sets the longest wait of one spawn for a running read. The
// returned func puts the old value back.
func SetSpawnWaitForTest(d time.Duration) (restore func()) {
	old := loginPATHSpawnWait
	loginPATHSpawnWait = d
	return func() { loginPATHSpawnWait = old }
}

// SpawnHorizonForTest returns the age of a read after which no spawn waits for
// it.
func SpawnHorizonForTest() time.Duration { return loginPATHSpawnHorizon }
