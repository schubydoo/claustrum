package main

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// TestSpawnSurvivesConfinementFailure covers spawn's confinement arm. Unlike the
// pipe seams next door this arm is deliberately NON-fatal: on Windows — the only
// OS where it can fail — a failed confinement costs the Job Object teardown and
// kill falls back to the parent alone, not the spawn (on Unix the process group
// from newSysProcAttr is signalled whatever the handle), so the process must still
// be registered with whatever group confinement handed back (nil from this seam;
// see the last paragraph for production). On Unix the
// call cannot fail at all — the process group came from newSysProcAttr and
// confineProcess returns a nil error unconditionally — so no fixture reaches the
// arm on the platform CI runs most.
//
// The assertions are on the OUTCOME, not on coverage: spawn must return the
// process and no error, p.group must be whatever confinement handed back rather
// than a handle spawn fabricated, and the warning must name the process and the
// reason — on Windows that log line is the only signal an operator gets that kill
// is now parent-only.
//
// The seam returns nil, so p.group is asserted nil HERE. That is the pass-through
// check, not a claim about production: Windows' confineProcess returns
// &procGroup{} with a zero job handle on every failure path, never nil (its
// signal() then falls back to the parent). Both are nil-safe.
func TestSpawnSurvivesConfinementFailure(t *testing.T) {
	errConfine := errors.New("job object: access is denied")
	old := confineProc
	t.Cleanup(func() { confineProc = old })
	confineProc = func(*os.Process) (*procGroup, error) { return nil, errConfine }

	m := newTestProcManager(t)
	t.Cleanup(m.killAll)
	c, _ := pipeConn(t)
	echo, env := helperCommand(t, "echo")

	var p *managedProc
	out := captureLog(t, func() {
		var err error
		if p, err = m.spawn(c, "confine", echo, []string{"hi"}, "", env); err != nil {
			t.Fatalf("spawn = %v, want success: a confinement failure is not fatal", err)
		}
	})
	if p == nil {
		t.Fatal("spawn returned a nil process despite reporting success")
	}
	if p.group != nil {
		t.Errorf("p.group = %v, want nil (the handle the seam returned)", p.group)
	}
	if want := "process-group confinement failed for confine: " + errConfine.Error(); !strings.Contains(out, want) {
		t.Errorf("missing warning %q\n--- captured ---\n%s", want, out)
	}
}
