//go:build unix

package main

import (
	"encoding/base64"
	"testing"
	"time"
)

// process.killAndWait escalates to SIGKILL when the graceful signal is ignored,
// and reports escalated:true. The child (ignore-term helper) ignores SIGTERM;
// after killGrace elapses the daemon force-kills it. Unix-only: escalation is a
// POSIX process-group concern.
func TestKillAndWaitEscalation(t *testing.T) {
	old := killGrace
	killGrace = 300 * time.Millisecond
	t.Cleanup(func() { killGrace = old })

	sock := startSocketServer(t)
	cl := dial(t, sock)
	cl.call(spawnReqArgs(t, 1, "IG", "ignore-term"))

	// Kill only once the child has installed its SIGTERM-ignore handler.
	cl.wait(func() bool {
		for _, f := range cl.fr {
			if f.ProcessID == "IG" && f.Stream == "stdout" {
				if b, _ := base64.StdEncoding.DecodeString(f.Data); string(b) == "ready\n" {
					return true
				}
			}
		}
		return false
	})

	var kw killAndWaitResult
	decodeReply(t, cl.call(authed(`{"jsonrpc":"2.0","id":2,"method":"process.killAndWait","params":{"id":"IG"}}`)), &kw)
	if !kw.Found || !kw.Died || !kw.Escalated {
		t.Errorf("killAndWait escalation = %+v, want found&died&escalated", kw)
	}
	if kw.AlreadyExited {
		t.Errorf("killAndWait escalation reported alreadyExited: %+v", kw)
	}
}
