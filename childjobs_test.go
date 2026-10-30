package main

import "testing"

// TestSpawnDoesNotInheritNoJobsSwitch: a daemon started with CLAUDE_SSH_NO_JOBS=1 gives
// a spawned child no CLAUDE_SSH_NO_JOBS, on every system, as 52d82f0a does (measured on
// Linux, macOS and Windows). The switch also turns the jobs off. A marker variable of the
// daemon is the control: it arrives. A caller env param of that name arrives too
// (claustrum's own, not measured). The child is the lookupenv helper of this test binary.
func TestSpawnDoesNotInheritNoJobsSwitch(t *testing.T) {
	const marker = "CLAUSTRUM_TEST_JOBS_MARK"
	t.Setenv(childJobsOffEnv, "1")
	t.Setenv(marker, "1")
	captureLogBuf(t)
	s, sock := bootLineServer(t, false)
	t.Cleanup(func() { s.signalShutdown(); s.closeAll(sock) })
	if s.procs.jobs != jobsOff {
		t.Errorf("jobs = %d with the switch, want jobsOff", s.procs.jobs)
	}
	cases := []struct{ id, name, param, want string }{
		{"nojobs", childJobsOffEnv, "", childJobsOffEnv + " absent"},
		{"mark", marker, "", marker + " set 1"},
		{"param", childJobsOffEnv, "x", childJobsOffEnv + " set x"},
	}
	for _, tc := range cases {
		c, frames := pipeConn(t)
		exe, env := helperCommand(t, "lookupenv")
		if tc.param != "" {
			env[tc.name] = tc.param
		}
		if _, err := s.procs.spawn(c, tc.id, exe, []string{tc.name}, "", env, false); err != nil {
			t.Fatalf("spawn: %v", err)
		}
		if got := firstStdout(t, frames); got != tc.want {
			t.Errorf("child %s saw %q, want %q", tc.id, got, tc.want)
		}
	}
}
