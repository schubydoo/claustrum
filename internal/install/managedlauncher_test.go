package install

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/schubydoo/claustrum/internal/sysproc"
)

// The managed launcher tests that run on every OS. The rows here are from the 89cb6289 captures, on Linux and macOS. The
// fixture-driven -install and -probe-cli rows are in managedlauncher_install_unix_test.go, and the Windows rows in
// managedlauncher_windows_test.go. The resolve and spawn rows are in the tests of package main.

// TestManagedLauncherParseValue pins the tokenizer and the JSON-array form, row by
// row. The value is the decoded settings string, so a JSON escape in a row's file
// is already decoded here.
func TestManagedLauncherParseValue(t *testing.T) {
	const w = "/fx/bin/wrap"
	cases := []struct {
		name, value string
		argv        []string
		reason      string
	}{
		{"a quoted argument with a blank", w + ` --x "a b"`, []string{w, "--x", "a b"}, ""},
		{"a quoted program and a quoted semicolon", `"` + w + `" "a;b"`, []string{w, "a;b"}, ""},
		{"an unterminated quote", w + ` "abc`, nil, managedUnterminated},
		{"escapes in quotes", w + ` "a\"b" "c\\d" "e\nf"`, []string{w, `a"b`, `c\d`, `e\nf`}, ""},
		{"a backslash quote outside quotes", w + ` a\"b c"`, []string{w, `a\b c`}, ""},
		{"single quotes do not group", w + ` 'a b'`, []string{w, "'a", "b'"}, ""},
		{"a no-break space splits", w + "\u00a0a", []string{w, "a"}, ""},
		{"a byte order mark splits", w + "\ufeffa", []string{w, "a"}, ""},
		{"a next-line character does not split", w + "\u0085a", []string{w + "\u0085a"}, ""},
		{"an empty token", w + ` ""`, nil, managedEmptyToken},
		{"an empty quoted program", `""`, nil, managedNoLauncher},
		{"a replacement character", w + " a\ufffd", []string{w, "a\ufffd"}, ""},
		{"blanks only", "   ", nil, managedNoLauncher},
		{"a JSON array", `["` + w + `","a b"]`, []string{w, "a b"}, ""},
		{"a JSON array after blanks", `  ["` + w + `"]`, []string{w}, ""},
		{"an empty JSON element", `["` + w + `",""]`, nil, managedEmptyElement},
		{"a JSON array of one empty string", `[""]`, nil, managedNoLauncher},
		{"an empty JSON array", `[]`, nil, managedNoLauncher},
		{"a JSON number", `["` + w + `",1]`, nil, managedNotStrings},
		{"a JSON array cut short", `[`, nil, managedBadJSONForm},
		{"text after a JSON array", `["` + w + `"] x`, nil, managedBadJSONForm},
		{"JSON with HTML characters and a line separator", `["` + w + `","<a&b>","x\u2028y"]`, []string{w, "<a&b>", "x\u2028y"}, ""},
	}
	for _, mc := range []string{";", "&", "|", "<", ">", "$", "`", "(", ")"} {
		cases = append(cases, struct {
			name, value string
			argv        []string
			reason      string
		}{"a metacharacter " + mc, w + " a" + mc, nil, managedMetachar})
	}
	for _, c := range cases {
		argv, reason := parseManagedLauncherValue(c.value)
		if reason != c.reason || !slices.Equal(argv, c.argv) {
			t.Errorf("%s %q: argv %q reason %q, want argv %q reason %q", c.name, c.value, argv, reason, c.argv, c.reason)
		}
	}
}

// TestManagedLauncherTexts pins every text the managed launcher emits, byte for
// byte, as the captures show them. The em dash (e2 80 94) and the ellipsis (e2
// 80 a6) are raw bytes, so the hex forms are checked too.
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

// TestManagedLauncherShownCut pins the cut of a shown launcher: a 300-byte launcher path with a
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
		t.Errorf("cut:\n got %q\nwant %q", got, want)
	}
	if s := strings.Repeat("a", 256); managedShown(s) != s {
		t.Error("a 256-byte path must be shown whole (claustrum's limit)")
	}
	if s := strings.Repeat("a", 257); managedShown(s) != strings.Repeat("a", 256)+"\u2026" {
		t.Errorf("a 257-byte path must keep 256 bytes, got %q", managedShown(s))
	}
}

// TestManagedLauncherEnvStrip pins a measured rule: every child loses the gate and
// the E2E variable, and a launched child also loses CLAUDE_CODE_PROCESS_WRAPPER.
func TestManagedLauncherEnvStrip(t *testing.T) {
	env := []string{"A=1", "CLAUDE_SSH_MANAGED_LAUNCHER=1", "CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR=x", "CLAUDE_CODE_PROCESS_WRAPPER=y", "B=2"}
	if got, want := StripEnv(slices.Clone(env), false), []string{"A=1", "CLAUDE_CODE_PROCESS_WRAPPER=y", "B=2"}; !slices.Equal(got, want) {
		t.Errorf("without a launcher: %q, want %q", got, want)
	}
	if got, want := StripEnv(slices.Clone(env), true), []string{"A=1", "B=2"}; !slices.Equal(got, want) {
		t.Errorf("with a launcher: %q, want %q", got, want)
	}
}

// TestCappedTextKeepsHeadAndCounts pins a measured rule: 10000 bytes of stderr keep 8192,
// then "\n[\u2026 1808 more bytes]". The kept part is the head: claustrum's choice (all
// 10000 measured bytes were E, so head and tail looked the same).
func TestCappedTextKeepsHeadAndCounts(t *testing.T) {
	c := &sysproc.CappedText{Max: managedRunStderrCap}
	_, _ = c.Write(bytes.Repeat([]byte("E"), 5000))
	_, _ = c.Write(bytes.Repeat([]byte("E"), 5000))
	want := strings.Repeat("E", 8192) + "\n[\xe2\x80\xa6 1808 more bytes]"
	if got := c.String(); got != want {
		t.Errorf("10000 bytes: len %d, tail %q; want len %d, tail %q", len(got), got[8180:], len(want), want[8180:])
	}
	c = &sysproc.CappedText{Max: 4}
	_, _ = c.Write([]byte("ab"))
	_, _ = c.Write([]byte("cdef"))
	if got := c.String(); got != "abcd\n[\xe2\x80\xa6 2 more bytes]" {
		t.Errorf("head across writes: %q", got)
	}
	c = &sysproc.CappedText{Max: 4}
	_, _ = c.Write([]byte("abcd"))
	if got := c.String(); got != "abcd" {
		t.Errorf("exactly at the cap: %q, want no tail", got)
	}
}

// TestManagedRunReasonExitStatus pins the launcherReason of a launcher that exits
// non-zero: "exit status 3" (measured).
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

// TestManagedLauncherRefusalAcrossFiles pins how a refusal of a value that is not a
// string meets the other values (b3081b51, measured on Linux). A later file's
// refusal leaves an earlier value in place. A later value replaces an earlier
// refusal, and a later refusal replaces an earlier one. In one file a string value
// of either key wins over a refused value of the other key. The last two rows are
// claustrum's choices (not measured): an env refusal wins over a processWrapper
// refusal, and over a processWrapper of "".
func TestManagedLauncherRefusalAcrossFiles(t *testing.T) {
	const tail = " must be one string (the launcher and its arguments), not "
	for _, c := range []struct {
		name, base, dropIn string
		status, reason     string
		fromDropIn         bool
	}{
		{"a later refusal keeps the earlier value", `{"processWrapper":"wrap"}`, `{"processWrapper":5}`, StatusUnusable, managedNotAbsolute, false},
		{"a later value replaces a refusal", `{"processWrapper":5}`, `{"processWrapper":"wrap"}`, StatusUnusable, managedNotAbsolute, true},
		{"a later refusal replaces a refusal", `{"processWrapper":5}`, `{"processWrapper":[]}`, StatusUnusable, "processWrapper" + tail + "an array", true},
		{"a later refusal after no value", `{}`, `{"processWrapper":5}`, StatusUnusable, "processWrapper" + tail + "a number", true},
		{"a string env value wins over a processWrapper of 5", `{"env":{"CLAUDE_CODE_PROCESS_WRAPPER":"wrap"},"processWrapper":5}`, ``, StatusUnusable, managedNotAbsolute, false},
		{"a string processWrapper wins over an env value of 5", `{"processWrapper":"wrap","env":{"CLAUDE_CODE_PROCESS_WRAPPER":5}}`, ``, StatusUnusable, managedNotAbsolute, false},
		{"a string processWrapper wins over an env value of true", `{"processWrapper":"wrap","env":{"CLAUDE_CODE_PROCESS_WRAPPER":true}}`, ``, StatusUnusable, managedNotAbsolute, false},
		{"an env refusal wins over a processWrapper refusal", `{"env":{"CLAUDE_CODE_PROCESS_WRAPPER":null},"processWrapper":{}}`, ``, StatusUnusable, "env.CLAUDE_CODE_PROCESS_WRAPPER" + tail + "null", false},
		{"an env refusal wins over a processWrapper of empty text", `{"env":{"CLAUDE_CODE_PROCESS_WRAPPER":[]},"processWrapper":""}`, ``, StatusUnusable, "env.CLAUDE_CODE_PROCESS_WRAPPER" + tail + "an array", false},
	} {
		dir := t.TempDir()
		base := filepath.Join(dir, managedSettingsBase)
		if err := os.WriteFile(base, []byte(c.base), 0o644); err != nil {
			t.Fatal(err)
		}
		source := base
		if c.dropIn != "" {
			dropDir := filepath.Join(dir, managedSettingsDropIns)
			if err := os.Mkdir(dropDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dropDir, "10.json"), []byte(c.dropIn), 0o644); err != nil {
				t.Fatal(err)
			}
			if c.fromDropIn {
				source = filepath.Join(dropDir, "10.json")
			}
		}
		got := resolveManagedLauncherIn(dir, "/opt/claude/cli")
		if got.Status != c.status || got.Reason != c.reason || got.Source != source {
			t.Errorf("%s: status %q reason %q source %q, want %q %q %q", c.name, got.Status, got.Reason, got.Source, c.status, c.reason, source)
		}
	}
}
