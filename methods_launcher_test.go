package main

import (
	"strings"
	"testing"

	"github.com/schubydoo/claustrum/internal/install"
	"github.com/schubydoo/claustrum/internal/rpc"
)

// TestLauncherCheckLogRepeatsArm pins both arms of noteChecked. With logsRepeats
// each refusal logs, a repeat too, as on Windows (claustrum's own). Without it a
// repeat logs no line until a usable answer comes between, as on Linux and macOS
// (4c111e86, measured).
func TestLauncherCheckLogRepeatsArm(t *testing.T) {
	refused := rpc.LauncherCheckResult{Status: install.StatusUnusable, Reason: "arm-test reason"}
	usable := rpc.LauncherCheckResult{Status: "usable"}
	line := "[LauncherHandler] the launcher that the desktop names is refused: arm-test reason\n"
	for _, c := range []struct {
		logsRepeats bool
		want        int
	}{{true, 3}, {false, 2}} {
		logs := captureLogBuf(t)
		var l managedLauncherLog
		for _, res := range []rpc.LauncherCheckResult{refused, refused, usable, refused} {
			l.noteChecked(res, c.logsRepeats)
		}
		if n := strings.Count(logs.String(), line); n != c.want {
			t.Errorf("logsRepeats %v: %d lines, want %d:\n%s", c.logsRepeats, n, c.want, logs.String())
		}
	}
}

// TestLauncherCheckResetArm pins resetChecked: after it an equal refusal logs again,
// as after an answer with no launcherCheck on macOS (4c111e86, measured on macOS).
func TestLauncherCheckResetArm(t *testing.T) {
	logs := captureLogBuf(t)
	refused := rpc.LauncherCheckResult{Status: install.StatusUnusable, Reason: "reset-test reason"}
	line := "[LauncherHandler] the launcher that the desktop names is refused: reset-test reason\n"
	var l managedLauncherLog
	l.noteChecked(refused, false)
	l.noteChecked(refused, false)
	l.resetChecked()
	l.noteChecked(refused, false)
	if n := strings.Count(logs.String(), line); n != 2 {
		t.Errorf("%d lines, want 2:\n%s", n, logs.String())
	}
}
