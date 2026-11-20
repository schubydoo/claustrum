package main

import (
	"net"
	"testing"

	"github.com/schubydoo/claustrum/internal/conn"
	"github.com/schubydoo/claustrum/internal/supervisor"
)

// TestSpawnReplyOutsideJob: while the start test of the jobs is pending or after it
// failed, a spawn answers {"success":true,"outsideJob":true} (measured on Windows
// against 4c111e86). A test that passed and a daemon with the jobs off answer
// {"success":true}. The state is set before the connection is served, so the rule is
// pinned on every system.
func TestSpawnReplyOutsideJob(t *testing.T) {
	for _, tc := range []struct {
		name  string
		jobs  supervisor.ChildJobs
		state supervisor.JobTestState
		want  string
	}{
		{"failed", supervisor.JobsOn, supervisor.JobTestFailed, `{"jsonrpc":"2.0","id":1,"result":{"success":true,"outsideJob":true}}`},
		{"pending", supervisor.JobsOn, supervisor.JobTestPending, `{"jsonrpc":"2.0","id":1,"result":{"success":true,"outsideJob":true}}`},
		{"passed", supervisor.JobsOn, supervisor.JobTestPassed, `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`},
		{"off", supervisor.JobsOff, supervisor.JobTestFailed, `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			t.Cleanup(s.procs.KillAll)
			s.procs.Jobs, s.procs.JobTest = tc.jobs, supervisor.NewJobTestForTest(tc.state)
			srv, client := net.Pipe()
			t.Cleanup(func() { _ = client.Close() })
			serveConnForTest(t, s, conn.New(srv, nil, 0, nil, nil, nil))
			cl := &testClient{t: t, nc: client}
			go cl.readLoop()
			if got := string(cl.call(linkSpawnReq(t, 1, "oj", "echo", []string{"hi"}, nil))); got != tc.want {
				t.Errorf("spawn reply = %s\nwant %s", got, tc.want)
			}
		})
	}
}
