//go:build unix

package main

import (
	"encoding/base64"
	"testing"
	"time"
)

// spawnIgnoreTerm spawns the ignore-term helper (ignores SIGTERM) and blocks until
// it announces readiness, so a following killAndWait exercises the grace/escalation
// path rather than an immediate death. Returns the client.
func spawnIgnoreTerm(t *testing.T, cl *testClient, id string, reqID int) {
	t.Helper()
	cl.call(spawnReqArgs(t, reqID, id, "ignore-term"))
	cl.wait(func() bool {
		for _, f := range cl.fr {
			if f.ProcessID == id && f.Stream == "stdout" {
				if b, _ := base64.StdEncoding.DecodeString(f.Data); string(b) == "ready\n" {
					return true
				}
			}
		}
		return false
	})
}

func killWait(t *testing.T, cl *testClient, body string) killAndWaitResult {
	t.Helper()
	var kw killAndWaitResult
	decodeReply(t, cl.call(authed(body)), &kw)
	return kw
}

// process.killAndWait escalates to SIGKILL when the graceful signal is ignored,
// reporting escalated:true. Unix-only: escalation is a POSIX process-group concern.
func TestKillAndWaitEscalation(t *testing.T) {
	old := defaultKillWaitMs
	defaultKillWaitMs = 300
	t.Cleanup(func() { defaultKillWaitMs = old })

	sock := startSocketServer(t)
	cl := dial(t, sock)
	spawnIgnoreTerm(t, cl, "IG", 1)

	kw := killWait(t, cl, `{"jsonrpc":"2.0","id":2,"method":"process.killAndWait","params":{"id":"IG"}}`)
	if !kw.Found || !kw.Died || !kw.Escalated {
		t.Errorf("killAndWait escalation = %+v, want found&died&escalated", kw)
	}
	if kw.AlreadyExited {
		t.Errorf("killAndWait escalation reported alreadyExited: %+v", kw)
	}
}

// escalate:false leaves a stubborn (SIGTERM-ignoring) process running after the
// grace and reports died:false with no escalation.
func TestKillAndWaitNoEscalate(t *testing.T) {
	old := defaultKillWaitMs
	defaultKillWaitMs = 300
	t.Cleanup(func() { defaultKillWaitMs = old })

	sock := startSocketServer(t)
	cl := dial(t, sock)
	spawnIgnoreTerm(t, cl, "NE", 1)

	kw := killWait(t, cl, `{"jsonrpc":"2.0","id":2,"method":"process.killAndWait","params":{"id":"NE","escalate":false,"timeoutMs":200}}`)
	if !kw.Found || kw.Died || kw.Escalated {
		t.Errorf("killAndWait escalate:false = %+v, want found, died:false, no escalated", kw)
	}
	// The process must still be alive (reattach reports running:true).
	var ra reattachResult
	decodeReply(t, cl.call(authed(`{"jsonrpc":"2.0","id":3,"method":"process.reattach","params":{"id":"NE","fromSeq":0}}`)), &ra)
	if !ra.Found || !ra.Running {
		t.Errorf("after escalate:false the process = %+v, want found && running", ra)
	}
	// Clean up: hard kill.
	cl.call(authed(`{"jsonrpc":"2.0","id":4,"method":"process.kill","params":{"id":"NE","signal":"KILL"}}`))
}

// timeoutMs overrides the grace: a tiny timeoutMs escalates well before the 3s
// default would. Asserted structurally (not by wall-clock) — the escalation still
// happens, and it happens under the shrunk default so the test stays fast.
func TestKillAndWaitTimeoutMs(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	spawnIgnoreTerm(t, cl, "TO", 1)

	// A 150ms grace with the process-default (3s) untouched: if timeoutMs were
	// ignored, this would block ~3s; it returns promptly with escalation.
	kw := killWait(t, cl, `{"jsonrpc":"2.0","id":2,"method":"process.killAndWait","params":{"id":"TO","timeoutMs":150}}`)
	if !kw.Found || !kw.Died || !kw.Escalated {
		t.Errorf("killAndWait timeoutMs=150 = %+v, want found&died&escalated", kw)
	}
}

// The clamp must reach the WAIT, not merely exist. clampKillWaitMs is unit-tested
// separately; that says nothing about whether processKillAndWait calls it, and a
// call site that passed timeoutMs through raw would leave every other test green.
//
// Driven with escalate:false so the observable is the grace alone: the child
// survives, the reply is {found:true,died:false}, and the elapsed time is the
// clamped value rather than the requested one. Unix-only, because it needs a
// process that can ignore SIGTERM.
func TestKillAndWaitClampReachesTheGrace(t *testing.T) {
	oldMax := maxKillWaitMs
	maxKillWaitMs = 250 // stand-in ceiling; the real one is 30s and untestable in CI
	t.Cleanup(func() { maxKillWaitMs = oldMax })

	sock := startSocketServer(t)
	cl := dial(t, sock)
	spawnIgnoreTerm(t, cl, "CLAMP", 1)

	start := time.Now()
	// 3000ms, not 8000: the shared harness aborts a read at 5s (harness_test.go),
	// so an 8s unclamped reply never arrives and the failure surfaces as a generic
	// harness timeout instead of the diagnostic below. Chosen so all four numbers
	// are comfortably separated — clamped ~0.25s, threshold 1.5s, unclamped ~3.0s,
	// harness cap 5s.
	kw := killWait(t, cl, `{"jsonrpc":"2.0","id":2,"method":"process.killAndWait",`+
		`"params":{"id":"CLAMP","signal":"SIGTERM","timeoutMs":3000,"escalate":false}}`)
	elapsed := time.Since(start)

	if !kw.Found || kw.Died {
		t.Errorf("killAndWait = %+v, want found:true died:false (escalate:false spares it)", kw)
	}
	// The requested grace was 3s and the ceiling is 250ms. A raw pass-through
	// cannot come in under 1.5s; the clamped path takes ~0.25s.
	if elapsed > 1500*time.Millisecond {
		t.Errorf("killAndWait waited %v for a clamped 250ms grace — the clamp is not "+
			"reaching the wait, so timeoutMs is being honored verbatim", elapsed)
	}
	// And it must actually have waited the grace, not returned instantly: an
	// instant return would mean the child died on SIGTERM and the fixture is
	// wrong, which would make the assertion above pass for the wrong reason.
	if elapsed < 200*time.Millisecond {
		t.Errorf("killAndWait returned in %v, faster than the 250ms grace — the "+
			"ignore-term child cannot have survived, so this proves nothing", elapsed)
	}
}
