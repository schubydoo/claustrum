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
// answers the file check. "path":5 is measured on Windows (4c111e86). Other member
// values, such as a string sha256, are claustrum's own. An orManagedFolder that is no
// bool gets Invalid params either way (13047a51, measured on Linux, macOS and Windows). Without it, as on Linux and macOS, such a member gets Invalid params
// (claustrum's own, not measured). A launcherCheck that is no object gets Invalid
// params either way, and null counts as absent. On Windows that is measured for a
// string, "", a number, true, [], an array of one object and null (4c111e86). The
// other values, and Linux and macOS, are claustrum's own, not measured.
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
		{true, `{"sha256":"x"}`, "unusable"},
		{true, `{"orManagedFolder":"yes"}`, invalid},
		{false, `{"orManagedFolder":"yes"}`, invalid},
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

// TestLauncherResolveOrManagedFolderPinned pins the -32602 of 13047a51 for an
// orManagedFolder with a digest (measured on Linux, macOS and Windows, before the host
// refusal). With an ownerUid of 0, as false, or as a top-level member it is no
// refusal (measured on Linux, an ownerUid of 0 also on Windows). An ownerUid of 5 is claustrum's own, not
// measured. Both arms of the decode answer alike.
func TestLauncherResolveOrManagedFolderPinned(t *testing.T) {
	const pinned = `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"orManagedFolder can't be set with sha256 or ownerUid"}}`
	digest := `["` + strings.Repeat("0", 64) + `"]`
	prev := checkTakesAnyMembers
	t.Cleanup(func() { checkTakesAnyMembers = prev })
	for _, anyMembers := range []bool{false, true} {
		checkTakesAnyMembers = anyMembers
		s := newTestServer(t)
		for _, row := range []struct {
			params  string
			refused bool
		}{
			{`"path":"/x/l","launcherCheck":{"orManagedFolder":true,"sha256":` + digest + `}`, true},
			{`"path":"/x/l","launcherCheck":{"orManagedFolder":true,"sha256":` + digest + `,"rootsLink":true}`, true},
			{`"launcherCheck":{"orManagedFolder":true,"ownerUid":5}`, true},
			{`"path":"/x/l","launcherCheck":{"orManagedFolder":true,"ownerUid":0}`, false},
			{`"path":"/x/l","launcherCheck":{"orManagedFolder":false,"sha256":` + digest + `}`, false},
			{`"path":"/x/l","orManagedFolder":true,"launcherCheck":{"sha256":` + digest + `}`, false},
		} {
			got := dispatchRaw(t, s, authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":{"cliPath":"x",`+row.params+`}}`))
			if (got == pinned) != row.refused {
				t.Errorf("anyMembers %v, %s: %s, refused %v", anyMembers, row.params, got, row.refused)
			}
			if !row.refused && !strings.Contains(got, `"result":{"status":"unusable"`) {
				t.Errorf("anyMembers %v, %s: %s, want an unusable answer", anyMembers, row.params, got)
			}
		}
	}
}
