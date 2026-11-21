package main

import (
	"strings"
	"testing"

	"github.com/schubydoo/claustrum/internal/install"
	"github.com/schubydoo/claustrum/internal/rpc"
)

// TestLauncherCheckLogRepeats pins noteChecked: a repeat of the last reason logs no
// line until a usable answer comes between (4c111e86, measured on Linux). On
// Windows 27 refusals in a row log one line (4c111e86, measured on Windows).
func TestLauncherCheckLogRepeats(t *testing.T) {
	refused := rpc.LauncherCheckReply{Status: install.StatusUnusable, Reason: "arm-test reason"}
	usable := rpc.LauncherCheckReply{Status: "usable"}
	line := "[LauncherHandler] the launcher that the desktop names is refused: arm-test reason\n"
	logs := captureLogBuf(t)
	var l managedLauncherLog
	for _, res := range []rpc.LauncherCheckReply{refused, refused, usable, refused} {
		l.noteChecked(res)
	}
	if n := strings.Count(logs.String(), line); n != 2 {
		t.Errorf("%d lines, want 2:\n%s", n, logs.String())
	}
}

// TestLauncherCheckAnyMembers pins the decode of launcherCheck. With
// checkTakesAnyMembers, as on Windows, a member of any type passes and the resolve
// answers the file check. "path":5 and "orManagedFolder":"yes" are measured on
// Windows (4c111e86), and other member values, such as "sha256":"x", are
// claustrum's own. Without it, as on Linux
// and macOS, such a member gets Invalid params (claustrum's own, not measured). A
// launcherCheck that is no object gets Invalid params either way, and null counts
// as absent (claustrum's own, not measured).
func TestLauncherCheckAnyMembers(t *testing.T) {
	const invalid = `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid params"}}`
	const none = `{"jsonrpc":"2.0","id":1,"result":{"status":"none"}}`
	prev := checkTakesAnyMembers
	t.Cleanup(func() { checkTakesAnyMembers = prev })
	for _, row := range []struct {
		anyMembers bool
		check      string
		want       string
	}{
		{true, `{"path":5}`, "unusable"},
		{true, `{"orManagedFolder":"yes","sha256":"x"}`, "unusable"},
		{true, `"x"`, invalid},
		{true, `null`, none},
		{false, `{"path":5}`, invalid},
		{false, `"x"`, invalid},
		{false, `null`, none},
	} {
		checkTakesAnyMembers = row.anyMembers
		s := newTestServer(t)
		got := dispatchRaw(t, s, authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":{"cliPath":"x","launcherCheck":`+row.check+`}}`))
		if row.want == "unusable" {
			if !strings.Contains(got, `"result":{"status":"unusable"`) {
				t.Errorf("anyMembers %v, launcherCheck %s: %s, want an unusable answer", row.anyMembers, row.check, got)
			}
		} else if got != row.want {
			t.Errorf("anyMembers %v, launcherCheck %s: %s, want %s", row.anyMembers, row.check, got, row.want)
		}
	}
}

// TestLauncherCheckResetArm pins resetChecked: after it an equal refusal logs again,
// as after an answer with no launcherCheck on macOS (4c111e86, measured on macOS).
func TestLauncherCheckResetArm(t *testing.T) {
	logs := captureLogBuf(t)
	refused := rpc.LauncherCheckReply{Status: install.StatusUnusable, Reason: "reset-test reason"}
	line := "[LauncherHandler] the launcher that the desktop names is refused: reset-test reason\n"
	var l managedLauncherLog
	l.noteChecked(refused)
	l.noteChecked(refused)
	l.resetChecked()
	l.noteChecked(refused)
	if n := strings.Count(logs.String(), line); n != 2 {
		t.Errorf("%d lines, want 2:\n%s", n, logs.String())
	}
}
