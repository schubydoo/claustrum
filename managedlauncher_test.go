package main

import (
	"bytes"
	"os"
	"slices"
	"strings"
	"testing"
)

// The managed launcher tests that run on every OS. The rows named here are the
// Linux sub-steps of the 89cb6289 VM captures, on Linux and macOS. The fixture-driven resolve, spawn, -install and -probe-cli rows are in
// managedlauncher_unix_test.go, and the Windows rows in
// managedlauncher_windows_test.go.

// TestManagedLauncherParseValue pins the tokenizer and the JSON-array form, row by
// row. The value is the decoded settings string, so a JSON escape in a row's file
// is already decoded here.
func TestManagedLauncherParseValue(t *testing.T) {
	const w = "/fx/bin/wrap"
	cases := []struct {
		row, value string
		argv       []string
		reason     string
	}{
		{"R5", w + ` --x "a b"`, []string{w, "--x", "a b"}, ""},
		{"R10b", `"` + w + `" "a;b"`, []string{w, "a;b"}, ""},
		{"R10c", w + ` "abc`, nil, managedUnterminated},
		{"R10d", w + ` "a\"b" "c\\d" "e\nf"`, []string{w, `a"b`, `c\d`, `e\nf`}, ""},
		{"R10e", w + ` a\"b c"`, []string{w, `a\b c`}, ""},
		{"R10f", w + ` 'a b'`, []string{w, "'a", "b'"}, ""},
		{"R10g-00A0", w + "\u00a0a", []string{w, "a"}, ""},
		{"R10g-FEFF", w + "\ufeffa", []string{w, "a"}, ""},
		{"R10g-0085", w + "\u0085a", []string{w + "\u0085a"}, ""},
		{"R10h-1", w + ` ""`, nil, managedEmptyToken},
		{"R10h-2", `""`, nil, managedNoLauncher},
		{"R10i", w + " a\ufffd", []string{w, "a\ufffd"}, ""},
		{"R6d", "   ", nil, managedNoLauncher},
		{"R9a", `["` + w + `","a b"]`, []string{w, "a b"}, ""},
		{"R9b", `  ["` + w + `"]`, []string{w}, ""},
		{"R9c", `["` + w + `",""]`, nil, managedEmptyElement},
		{"R9d-1", `[""]`, nil, managedNoLauncher},
		{"R9d-2", `[]`, nil, managedNoLauncher},
		{"R9e", `["` + w + `",1]`, nil, managedNotStrings},
		{"R9f-1", `[`, nil, managedBadJSONForm},
		{"R9f-2", `["` + w + `"] x`, nil, managedBadJSONForm},
		{"R9g", `["` + w + `","<a&b>","x\u2028y"]`, []string{w, "<a&b>", "x\u2028y"}, ""},
	}
	for _, mc := range []string{";", "&", "|", "<", ">", "$", "`", "(", ")"} {
		cases = append(cases, struct {
			row, value string
			argv       []string
			reason     string
		}{"R10a " + mc, w + " a" + mc, nil, managedMetachar})
	}
	for _, c := range cases {
		argv, reason := parseManagedLauncherValue(c.value)
		if reason != c.reason || !slices.Equal(argv, c.argv) {
			t.Errorf("%s %q: argv %q reason %q, want argv %q reason %q", c.row, c.value, argv, reason, c.argv, c.reason)
		}
	}
}

// TestManagedLauncherTexts pins every text the managed launcher emits, byte for
// byte, as the VM captures show them. The em dash (e2 80 94) and the ellipsis
// (e2 80 a6) are raw bytes, so the hex forms are checked too.
func TestManagedLauncherTexts(t *testing.T) {
	texts := map[string]string{
		managedNoLauncher:                 "the value is set but contains no launcher \xe2\x80\x94 unset it to run without one, or set it to the absolute path of your launcher",
		managedEmptyElement:               "the JSON array contains an empty element \xe2\x80\x94 remove it, or fill in the value it was a placeholder for",
		managedNotStrings:                 "JSON form must be an array of strings",
		managedBadJSONForm:                "value starts with `[` but is not valid JSON",
		managedMetachar:                   "the value contains an unquoted shell metacharacter (one of ; | & $ ( ) ` < >) \xe2\x80\x94 it is an argv list, not a shell command",
		managedUnterminated:               "unterminated double quote",
		managedEmptyToken:                 "the value contains an empty `\"\"` token \xe2\x80\x94 remove it, or fill in the value it was a placeholder for",
		managedNotAbsolute:                "the launcher must be an absolute path, not a bare name resolved via PATH",
		managedOwnPath("/opt/claude/cli"): "launcher `/opt/claude/cli` is Claude Code's own path",
		managedScript("/fx/bin/x.js"):     "launcher `/fx/bin/x.js` is a script the SDK would run in place of Claude Code",
		managedNotExecutable("/fx/nope"):  "launcher `/fx/nope` does not exist or is not an executable regular file",
		managedIsDir:                      "is a directory",
		managedNotRegular:                 "not a regular file",
		managedTooLarge:                   "larger than 2 MiB",
		managedInvalidJSON:                "not valid JSON",
		managedNotObject:                  "not a JSON object",
		managedDropInsListed:              "the drop-in directory could not be listed: ",
		managedSpawnRelative:              "command must be an absolute path when a launcher is given",
		managedSpawnCannotUse:             "the managed launcher cannot be used: ",
		managedNoInterpreter:              "its interpreter was not found (the program on its #! line, or the loader of an ELF binary; a script saved with Windows CRLF line endings fails this way)",
		managedRunStopped:                 "did not exit within 33s and was stopped",
		managedFirstRunStopped:            "did not exit within 123s and was stopped",
		managedUnresponsiveText("/fx/l6"): "cli unresponsive: the installed Claude Code binary was started through the host's managed launcher /fx/l6 and the run did not answer --version within 33s (123s for a first run), so it was stopped; the launcher or the host is not letting it finish",
		managedStatusProbeFailed:          "probe_failed",
		managedStatusUnresponsive:         "unresponsive",
		managedLauncherGateEnv:            "CLAUDE_SSH_MANAGED_LAUNCHER",
		managedSettingsDirEnv:             "CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR",
		managedWrapperEnv:                 "CLAUDE_CODE_PROCESS_WRAPPER",
		managedSettingsBase:               "managed-settings.json",
		managedSettingsDropIns:            "managed-settings.d",
	}
	for got, want := range texts {
		if got != want {
			t.Errorf("text = %q, want %q", got, want)
		}
	}
	if managedSettingsMaxBytes != 2097152 {
		t.Errorf("size limit = %d, want 2097152", managedSettingsMaxBytes)
	}
	if managedRunStderrCap != 8192 {
		t.Errorf("stderr cap = %d, want 8192", managedRunStderrCap)
	}
}

// TestManagedLauncherShownCut pins the R11e cut: a 300-byte launcher path with a
// 3-byte character at bytes 254-256 keeps its first 254 bytes, then an ellipsis.
// The 256-byte limit itself is claustrum's choice, so the ASCII arms pin that
// choice, not a measurement.
func TestManagedLauncherShownCut(t *testing.T) {
	long := strings.Repeat("x", 254) + "\u20ac" + strings.Repeat("y", 43)
	if len(long) != 300 {
		t.Fatalf("fixture is %d bytes, want 300", len(long))
	}
	got := managedNotExecutable(long)
	want := "launcher `" + strings.Repeat("x", 254) + "\xe2\x80\xa6` does not exist or is not an executable regular file"
	if got != want {
		t.Errorf("R11e cut:\n got %q\nwant %q", got, want)
	}
	if s := strings.Repeat("a", 256); managedShown(s) != s {
		t.Error("a 256-byte path must be shown whole (claustrum's limit)")
	}
	if s := strings.Repeat("a", 257); managedShown(s) != strings.Repeat("a", 256)+"\u2026" {
		t.Errorf("a 257-byte path must keep 256 bytes, got %q", managedShown(s))
	}
}

// TestLauncherResolveParams pins the params checks of launcher.resolve (R1, R2,
// R3, C2, C3, the Linux and Windows recheck C2 rows). None of these reads a settings
// file, so they hold on every OS.
func TestLauncherResolveParams(t *testing.T) {
	s := newTestServer(t)
	cases := []struct{ line, want string }{
		{authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve"}`), `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid params"}}`},
		{authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":{}}`), `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"cliPath is required"}}`},
		{authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":{"cliPath":""}}`), `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"cliPath is required"}}`},
		{authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":null}`), `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"cliPath is required"}}`},
		{authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":{"cliPath":null}}`), `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"cliPath is required"}}`},
		{authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":{"path":"/x"}}`), `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"cliPath is required"}}`},
		{authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":{"cliPath":5}}`), `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid params"}}`},
		{authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":[]}`), `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid params"}}`},
		{authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.bogus","params":{}}`), `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Unknown method: launcher.bogus"}}`},
		{`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":{"cliPath":"/x"}}`, `{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"Unauthorized: invalid or missing auth token"}}`},
	}
	for _, c := range cases {
		if got := dispatchRaw(t, s, c.line); got != c.want {
			t.Errorf("%s\n got %s\nwant %s", c.line, got, c.want)
		}
	}
}

// TestManagedLauncherEnvStrip pins ENV-1 and ENV-2: every child loses the gate and
// the E2E variable, and a launched child also loses CLAUDE_CODE_PROCESS_WRAPPER.
func TestManagedLauncherEnvStrip(t *testing.T) {
	env := []string{"A=1", "CLAUDE_SSH_MANAGED_LAUNCHER=1", "CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR=x", "CLAUDE_CODE_PROCESS_WRAPPER=y", "B=2"}
	if got, want := stripManagedLauncherEnv(slices.Clone(env), false), []string{"A=1", "CLAUDE_CODE_PROCESS_WRAPPER=y", "B=2"}; !slices.Equal(got, want) {
		t.Errorf("without a launcher: %q, want %q", got, want)
	}
	if got, want := stripManagedLauncherEnv(slices.Clone(env), true), []string{"A=1", "B=2"}; !slices.Equal(got, want) {
		t.Errorf("with a launcher: %q, want %q", got, want)
	}
}

// TestSpawnLauncherDecodeErrors pins S10: a non-array launcher or a non-string
// element is -32602 Invalid params, with no [process.Manager] failure line.
func TestSpawnLauncherDecodeErrors(t *testing.T) {
	logs := captureLogBuf(t)
	s := newTestServer(t)
	for _, l := range []string{`"x"`, `[1]`} {
		line := authed(`{"jsonrpc":"2.0","id":1,"method":"process.spawn","params":{"id":"p1","command":"/x/echo","args":["hi"],"launcher":` + l + `}}`)
		if got, want := dispatchRaw(t, s, line), `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid params"}}`; got != want {
			t.Errorf("launcher %s: %s, want %s", l, got, want)
		}
	}
	// Only a spawn failure line counts. An earlier test's child still logs its
	// exit into this buffer at times.
	if strings.Contains(logs.String(), "[process.Manager] Failed to start process p1") {
		t.Errorf("a decode error must write no [process.Manager] line:\n%s", logs.String())
	}
}

// TestCappedTextKeepsHeadAndCounts pins INS-12: 10000 bytes of stderr keep 8192,
// then "\n[\u2026 1808 more bytes]". The kept part is the head: claustrum's choice
// (all 10000 measured bytes were E, so head and tail looked the same).
func TestCappedTextKeepsHeadAndCounts(t *testing.T) {
	c := &cappedText{max: managedRunStderrCap}
	_, _ = c.Write(bytes.Repeat([]byte("E"), 5000))
	_, _ = c.Write(bytes.Repeat([]byte("E"), 5000))
	want := strings.Repeat("E", 8192) + "\n[\xe2\x80\xa6 1808 more bytes]"
	if got := c.String(); got != want {
		t.Errorf("10000 bytes: len %d, tail %q; want len %d, tail %q", len(got), got[8180:], len(want), want[8180:])
	}
	c = &cappedText{max: 4}
	_, _ = c.Write([]byte("ab"))
	_, _ = c.Write([]byte("cdef"))
	if got := c.String(); got != "abcd\n[\xe2\x80\xa6 2 more bytes]" {
		t.Errorf("head across writes: %q", got)
	}
	c = &cappedText{max: 4}
	_, _ = c.Write([]byte("abcd"))
	if got := c.String(); got != "abcd" {
		t.Errorf("exactly at the cap: %q, want no tail", got)
	}
}

// TestManagedRunReasonExitStatus pins the launcherReason of a launcher that exits
// non-zero: "exit status 3" (I5).
func TestManagedRunReasonExitStatus(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUSTRUM_TEST_HELPER", "exit:3")
	r := runViaManagedLauncher([]string{exe}, "cli", managedRunBound)
	if r.hung || r.err == nil {
		t.Fatalf("run = %+v, want a failed run", r)
	}
	if got := managedRunReason(r.err); got != "exit status 3" {
		t.Errorf("reason = %q, want %q", got, "exit status 3")
	}
}

// TestProbeCLIUsageNamesTheLauncher pins USG-1: the -probe-cli usage line is the
// only -help difference, and it names the gate and __CLI_LAUNCHER__.
func TestProbeCLIUsageNamesTheLauncher(t *testing.T) {
	runMain(t, "-version")
	f := lastMainFlagSet.Lookup("probe-cli")
	if f == nil {
		t.Fatal("no -probe-cli flag")
	}
	const want = "Run the bounded --version probe on this CLI binary and exit 0: prints nothing if it runs, __CLI_HUNG__ if it had to be killed, __CLI_BAD__ if it is missing or does not run; with CLAUDE_SSH_MANAGED_LAUNCHER=1 it runs through the host's managed launcher and prints __CLI_LAUNCHER__ when the launcher is unusable, the managed settings are unreadable, or the launcher's run of it failed (reason on stderr)"
	if f.Usage != want {
		t.Errorf("usage = %q\nwant    %q", f.Usage, want)
	}
}
