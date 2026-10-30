package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"time"
)

// A farewell is the optional last input of process.kill and process.killAndWait.
// The daemon writes the decoded `farewell` bytes to the stdin of the child and
// sends the signal only after `graceMs`, unless the child ends first. Measured on
// Linux and macOS (build `52d82f0a`). Windows has no farewell row, so it runs the
// same code (claustrum's own, not measured).

// The values of the farewellEnd member of both replies.
const (
	farewellQueued  = "queued"
	farewellUnsent  = "unsent"
	farewellInvalid = "invalid"
	farewellLeft    = "left"
	farewellExpired = "expired"
	farewellCut     = "cut"
)

// farewellMaxBytes is the cap on the decoded farewell. 65536 bytes pass and 65537
// get "over the cap" (measured on Linux and macOS).
const farewellMaxBytes = 65536

// farewellGraceCapMs is the cap on graceMs: 45000 waits 30 s (measured on Linux).
// A var so a test can shrink it. Production never assigns it.
var farewellGraceCapMs = 30000

// farewellAsk is a valid farewell: the decoded bytes and the grace before the signal.
type farewellAsk struct {
	data  []byte
	grace time.Duration
}

// farewellHold is the pending farewell of one record. cut is closed by the kill
// that ends the farewell before its grace ran out. timer is the grace timer of a
// process.kill, nil for a process.killAndWait, which waits in its own request.
// Both are read and written under p.mu.
type farewellHold struct {
	cut   chan struct{}
	timer *time.Timer
}

// rawAbsent reports a member that the request left out or set to null. A null
// member counts as absent (measured on Linux).
func rawAbsent(raw json.RawMessage) bool {
	return len(raw) == 0 || bytes.Equal(raw, []byte("null"))
}

// readFarewell checks the two farewell members in the measured order (Linux).
// A member of the wrong type comes first, then a farewell with no graceMs, then a
// graceMs with no farewell, then the base64 test, then the cap. A negative graceMs
// counts as absent, and an empty farewell with graceMs 0 counts as no farewell at
// all (measured). claustrum reads graceMs 0 as absent and an empty farewell as
// absent in the other pairs too (claustrum's own, not measured). It returns a nil
// ask and an empty rule when the request asks for no farewell.
func readFarewell(farewell, graceMs json.RawMessage) (ask *farewellAsk, rule string) {
	var text string
	var ms int64
	if !rawAbsent(farewell) && json.Unmarshal(farewell, &text) != nil {
		return nil, "of the wrong type"
	}
	// 1.5 and "x" are of the wrong type (measured). An integer that does not fit
	// in 64 bits is too (claustrum's own, not measured).
	if !rawAbsent(graceMs) && json.Unmarshal(graceMs, &ms) != nil {
		return nil, "of the wrong type"
	}
	switch {
	case text == "" && ms <= 0:
		return nil, ""
	case ms <= 0:
		return nil, "a farewell with no graceMs"
	case text == "":
		return nil, "graceMs with no farewell"
	}
	data, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return nil, "not base64"
	}
	if len(data) > farewellMaxBytes {
		return nil, "over the cap"
	}
	ms = min(ms, int64(farewellGraceCapMs))
	return &farewellAsk{data: data, grace: time.Duration(ms) * time.Millisecond}, ""
}

// logFarewellNotSaid is the line of a farewell that fails a rule (measured on
// Linux and macOS). The signal goes at once after it.
func logFarewellNotSaid(id, rule string) {
	logInfof("[process] kill %s: its farewell is not said: %s", id, rule)
}

// holdFarewell marks a pending farewell on a live record that the daemon has not
// signalled yet. It stamps killedBy "client" before any signal, so an exit with
// code 0 after the farewell carries it (measured on Linux and macOS). It returns
// nil for a record that is gone, in its exit drain, already signalled, or that
// holds a pending farewell. The caller then sends the plain kill, which cuts the
// pending farewell, and answers "unsent". A record signalled after a plain kill
// gives "unsent" (measured on Linux and macOS). A second farewell on a pending one
// gives "unsent" (claustrum's own, not measured).
func (p *managedProc) holdFarewell() *farewellHold {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.running || p.reaped || p.cmd == nil || p.cmd.Process == nil || p.signalled || p.farewell != nil {
		return nil
	}
	if p.killedBy == "" {
		p.killedBy = "client"
	}
	h := &farewellHold{cut: make(chan struct{})}
	p.farewell = h
	return h
}

// dropFarewell ends a pending farewell without a cut. It reports whether h was
// still the pending one, so false means that another kill cut it.
func (p *managedProc) dropFarewell(h *farewellHold) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.farewell != h {
		return false
	}
	p.farewell = nil
	return true
}

// cutFarewellLocked ends the pending farewell of a record that gets a signal now.
// The caller holds p.mu.
func (p *managedProc) cutFarewellLocked() {
	h := p.farewell
	if h == nil {
		return
	}
	p.farewell = nil
	if h.timer != nil {
		h.timer.Stop()
	}
	close(h.cut)
}

// killWithFarewell is process.kill with a valid farewell. The reply is at once.
// The farewell goes to the stdin queue, and the signal of the request follows
// after the grace unless the child ended or another kill cut the farewell. It
// signals the same process group as kill, through signalIfLive.
func (m *procManager) killWithFarewell(id, signal string, ask farewellAsk) string {
	p := m.get(id)
	if p == nil {
		return farewellUnsent
	}
	h := p.holdFarewell()
	if h == nil {
		p.signalIfLive(signal, "client")
		return farewellUnsent
	}
	// Outside p.mu: applyStdin takes stdinMu before p.mu. The farewell does not
	// move the stdin offset of the record (claustrum's own, not measured).
	p.enqueueStdin(ask.data)
	p.mu.Lock()
	if p.farewell == h {
		h.timer = time.AfterFunc(ask.grace, func() {
			if p.dropFarewell(h) {
				p.signalIfLive(signal, "client")
			}
		})
	}
	p.mu.Unlock()
	return farewellQueued
}

// killAndWaitFarewell is process.killAndWait with a valid farewell. It waits the
// grace in the request. A child that ends in it gives "left" with no escalation.
// At the end of the grace the normal kill path runs and gives "expired". A kill
// that cuts the farewell gives "cut" at once (measured on Linux and macOS). Any
// state that holdFarewell refuses runs the normal kill path and gives "unsent".
func (m *procManager) killAndWaitFarewell(id, signal string, grace time.Duration, escalate bool, ask farewellAsk) (res killAndWaitResult) {
	p := m.get(id)
	if p == nil {
		return killAndWaitResult{FarewellEnd: farewellUnsent}
	}
	h := p.holdFarewell()
	if h == nil {
		res.Found, res.Died, res.AlreadyExited, res.Escalated = m.killAndWaitProc(p, signal, grace, escalate)
		res.FarewellEnd = farewellUnsent
		return res
	}
	p.mu.Lock()
	p.waiting++
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.waiting--
		p.mu.Unlock()
	}()
	p.enqueueStdin(ask.data)
	t := time.NewTimer(ask.grace)
	defer t.Stop()
	select {
	case <-p.done:
		p.dropFarewell(h)
		return killAndWaitResult{Found: true, Died: true, FarewellEnd: farewellLeft}
	case <-h.cut:
		// died is true at once, before the exit frame (measured on Linux). For a
		// child that outlives the signal of the cut it is claustrum's own, not measured.
		return killAndWaitResult{Found: true, Died: true, FarewellEnd: farewellCut}
	case <-t.C:
	}
	if !p.dropFarewell(h) {
		return killAndWaitResult{Found: true, Died: true, FarewellEnd: farewellCut}
	}
	res.Found, res.Died, res.AlreadyExited, res.Escalated = m.signalAndWait(p, signal, grace, escalate)
	res.FarewellEnd = farewellExpired
	return res
}
