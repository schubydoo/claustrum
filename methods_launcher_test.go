package main

import (
	"strings"
	"testing"

	"github.com/schubydoo/claustrum/internal/install"
	"github.com/schubydoo/claustrum/internal/rpc"
)

// TestLauncherCheckLogRepeatsArm pins both arms of noteChecked. With logsRepeats
// each refusal logs, a repeat too, as on macOS (c9125eb8, measured on macOS). Without
// it a repeat logs no line until a usable answer comes between, as on Linux
// (4c111e86, measured on Linux).
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
