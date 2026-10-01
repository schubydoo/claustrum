package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The tests of the daemon's own git environment (gitenv.go). Each row label names
// the f6010b97 capture row it comes from: rounds 1 to 4, validation 1 (V rows),
// validation 2 (val2 rows), validation 3 (val3 rows) and validation 4 (val4 rows).
// The control rows have no capture row. A "windows" or "macos" label names a row of
// that VM only. None of these tests run in parallel: they set the process
// environment.

// clearDaemonConfigEnv removes the GIT_CONFIG_COUNT family and GIT_ALLOW_PROTOCOL
// from the test's environment for the rest of the test.
func clearDaemonConfigEnv(t *testing.T) {
	t.Helper()
	names := []string{configCountName, allowProtocolVar, "GIT_CONFIG", "GIT_CONFIG_PARAMETERS"}
	for i := range 10 {
		names = append(names, configKeyPrefix+strconv.Itoa(i), configValPrefix+strconv.Itoa(i))
	}
	for _, k := range names {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
}

// plantEnv sets each KEY=VALUE of kvs in the test's environment.
func plantEnv(t *testing.T, kvs ...string) {
	t.Helper()
	for _, kv := range kvs {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
}

func TestCountFromText(t *testing.T) {
	for _, tc := range []struct {
		row, v string
		want   int
		ok     bool
	}{
		{"round2 A1 zero", "0", 0, true},
		{"round2 A7 empty", "", 0, true},
		{"round2 A3 leading zero", "01", 1, true},
		{"round2 A4 leading space", " 1", 1, true},
		{"round2 A6 plus sign", "+1", 1, true},
		{"round2 A14 two", "2", 2, true},
		{"round2 A2 minus one", "-1", 0, false},
		{"round2 A5 trailing space", "1 ", 0, false},
		{"round2 A11 above the int range", "99999999999999999999", 0, false},
		{"round2 A12 hex", "0x1", 0, false},
		{"round2 A13 decimal point", "1.0", 0, false},
		{"round1 B6 letter", "x", 0, false},
		{"validation V6a two leading spaces", "  1", 1, true},
		{"validation V6b leading tab", "\t1", 1, true},
		{"validation V6d space and plus", " +1", 1, true},
		{"validation V6h two zeros", "00", 0, true},
		{"validation V6c plus alone", "+", 0, false},
		{"validation V6e plus and space", "+ 1", 0, false},
		{"validation V6f two plus signs", "++1", 0, false},
		{"validation V6g trailing newline", "1\n", 0, false},
		{"validation V6 (macOS) tabs around", "\t1\t", 0, false},
		{"round4 R4a tab", "\t1", 1, true},
		{"round4 R4b newline", "\n1", 1, true},
		{"round4 R4c vertical tab", "\v1", 1, true},
		{"round4 R4d form feed", "\f1", 1, true},
		{"round4 R4e carriage return", "\r1", 1, true},
		{"round4 R4f space tab space", " \t 1", 1, true},
		{"round4 R4g no-break space", "\u00a01", 0, false},
		{"round4 R4h tab alone", "\t", 0, false},
		{"round4 R4i space alone", " ", 0, false},
	} {
		t.Run(tc.row, func(t *testing.T) {
			n, ok := countFromText(tc.v)
			if n != tc.want || ok != tc.ok {
				t.Errorf("countFromText(%q) = %d, %v, want %d, %v", tc.v, n, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestDaemonCountRefusalTexts(t *testing.T) {
	requireGit(t)
	resetUserExcludesCache(t)
	for _, tc := range []struct {
		row   string
		plant []string
		want  string // "" means no refusal
	}{
		{"round1 B6 letter", []string{"GIT_CONFIG_COUNT=x"},
			`config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG_COUNT "x" is not a count`},
		{"round2 A2 minus one", []string{"GIT_CONFIG_KEY_0=core.fileMode", "GIT_CONFIG_VALUE_0=false", "GIT_CONFIG_COUNT=-1"},
			`config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG_COUNT "-1" is not a count`},
		{"round2 A5 trailing space", []string{"GIT_CONFIG_KEY_0=core.fileMode", "GIT_CONFIG_VALUE_0=false", "GIT_CONFIG_COUNT=1 "},
			`config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG_COUNT "1 " is not a count`},
		{"round2 A11 above the int range", []string{"GIT_CONFIG_COUNT=99999999999999999999"},
			`config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG_COUNT "99999999999999999999" is not a count`},
		{"round2 A12 hex", []string{"GIT_CONFIG_COUNT=0x1"},
			`config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG_COUNT "0x1" is not a count`},
		{"round2 A13 decimal point", []string{"GIT_CONFIG_COUNT=1.0"},
			`config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG_COUNT "1.0" is not a count`},
		{"round4 R4g no-break space", []string{"GIT_CONFIG_KEY_0=core.fileMode", "GIT_CONFIG_VALUE_0=false", "GIT_CONFIG_COUNT=\u00a01"},
			`config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG_COUNT "\u00a01" is not a count`},
		{"round4 R4h tab alone", []string{"GIT_CONFIG_KEY_0=core.fileMode", "GIT_CONFIG_VALUE_0=false", "GIT_CONFIG_COUNT=\t"},
			`config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG_COUNT "\t" is not a count`},
		{"validation V7 KEY_01 is not pair 1", []string{"GIT_CONFIG_KEY_0=core.fileMode", "GIT_CONFIG_VALUE_0=false",
			"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_01=user.s4v7", "GIT_CONFIG_VALUE_01=S4KEEP_01"},
			`config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG pair 1 is incomplete`},
		{"round2 A8 pair 1 missing", []string{"GIT_CONFIG_KEY_0=core.fileMode", "GIT_CONFIG_VALUE_0=false", "GIT_CONFIG_COUNT=2"},
			`config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG pair 1 is incomplete`},
		{"round2 A9 value unset", []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.fileMode"},
			`config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG pair 0 is incomplete`},
		{"round2 A10 key unset", []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_VALUE_0=false"},
			`config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG pair 0 is incomplete`},
		{"round2 A1 zero", []string{"GIT_CONFIG_COUNT=0", "GIT_CONFIG_KEY_0=core.fileMode", "GIT_CONFIG_VALUE_0=false"}, ""},
		{"round2 A7 empty", []string{"GIT_CONFIG_COUNT=", "GIT_CONFIG_KEY_0=core.fileMode", "GIT_CONFIG_VALUE_0=false"}, ""},
		{"round2 A14 two pairs", []string{"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=core.fileMode", "GIT_CONFIG_VALUE_0=false",
			"GIT_CONFIG_KEY_1=core.fileMode", "GIT_CONFIG_VALUE_1=true"}, ""},
		{"round3 P3 Q15 empty key", []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=", "GIT_CONFIG_VALUE_0=x"}, ""},
		{"round3 P4 Q16 empty value", []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.fileMode", "GIT_CONFIG_VALUE_0="}, ""},
		{"round1 C0 unset", nil, ""},
	} {
		t.Run(tc.row, func(t *testing.T) {
			clearDaemonConfigEnv(t)
			plantEnv(t, tc.plant...)
			got, bad := daemonCountRefusal()
			if got != tc.want || bad != (tc.want != "") {
				t.Errorf("daemonCountRefusal() = %q, %v\nwant %q", got, bad, tc.want)
			}
		})
	}
}

// configEntries returns the GIT_CONFIG, GIT_CONFIG_PARAMETERS, GIT_CONFIG_COUNT and
// pair entries of env, sorted.
func configEntries(env []string) []string {
	var out []string
	for _, kv := range env {
		n := envName(kv)
		if isConfigPairName(n) || n == configCountName || n == "GIT_CONFIG" || n == "GIT_CONFIG_PARAMETERS" {
			out = append(out, kv)
		}
	}
	slices.Sort(out)
	return out
}

func sorted(s ...string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}

func TestHardenedGitEnvPairs(t *testing.T) {
	for _, tc := range []struct {
		row   string
		plant []string
		want  []string // the pinned call's entries
	}{
		{"round1 C0 unset", nil, []string{"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=hook.enabled", "GIT_CONFIG_VALUE_0=false",
			"GIT_CONFIG_KEY_1=hook.event", "GIT_CONFIG_VALUE_1="}},
		{"round1 C3 one inherited pair and stale names",
			[]string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=user.s4mark", "GIT_CONFIG_VALUE_0=S4KEEP_v",
				"GIT_CONFIG_KEY_1=S4KEEP_k1", "GIT_CONFIG_KEY_2=S4KEEP_k2", "GIT_CONFIG_VALUE_2=S4KEEP_v2"},
			[]string{"GIT_CONFIG_COUNT=3", "GIT_CONFIG_KEY_0=user.s4mark", "GIT_CONFIG_VALUE_0=S4KEEP_v",
				"GIT_CONFIG_KEY_1=hook.enabled", "GIT_CONFIG_VALUE_1=false", "GIT_CONFIG_KEY_2=hook.event", "GIT_CONFIG_VALUE_2="}},
		{"round1 C2 KEY_9 and VALUE_9 with no count", []string{"GIT_CONFIG_KEY_9=S4MARK_k", "GIT_CONFIG_VALUE_9=S4MARK_v"},
			[]string{"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=hook.enabled", "GIT_CONFIG_VALUE_0=false",
				"GIT_CONFIG_KEY_1=hook.event", "GIT_CONFIG_VALUE_1="}},
		{"windows C3b pairs above the count",
			[]string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=user.s4mark", "GIT_CONFIG_VALUE_0=S4KEEP_v",
				"GIT_CONFIG_VALUE_1=S4KEEP_v1", "GIT_CONFIG_KEY_5=S4KEEP_k5", "GIT_CONFIG_VALUE_5=S4KEEP_v5"},
			[]string{"GIT_CONFIG_COUNT=3", "GIT_CONFIG_KEY_0=user.s4mark", "GIT_CONFIG_VALUE_0=S4KEEP_v",
				"GIT_CONFIG_KEY_1=hook.enabled", "GIT_CONFIG_VALUE_1=false", "GIT_CONFIG_KEY_2=hook.event", "GIT_CONFIG_VALUE_2="}},
		{"windows C3c count 0", []string{"GIT_CONFIG_COUNT=0", "GIT_CONFIG_KEY_0=user.s4mark", "GIT_CONFIG_VALUE_0=S4KEEP_v"},
			[]string{"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=hook.enabled", "GIT_CONFIG_VALUE_0=false",
				"GIT_CONFIG_KEY_1=hook.event", "GIT_CONFIG_VALUE_1="}},
		{"round2 A3 leading zero", []string{"GIT_CONFIG_COUNT=01", "GIT_CONFIG_KEY_0=core.fileMode", "GIT_CONFIG_VALUE_0=false"},
			[]string{"GIT_CONFIG_COUNT=3", "GIT_CONFIG_KEY_0=core.fileMode", "GIT_CONFIG_VALUE_0=false",
				"GIT_CONFIG_KEY_1=hook.enabled", "GIT_CONFIG_VALUE_1=false", "GIT_CONFIG_KEY_2=hook.event", "GIT_CONFIG_VALUE_2="}},
		{"round2 A7 empty count", []string{"GIT_CONFIG_COUNT=", "GIT_CONFIG_KEY_0=core.fileMode", "GIT_CONFIG_VALUE_0=false"},
			[]string{"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=hook.enabled", "GIT_CONFIG_VALUE_0=false",
				"GIT_CONFIG_KEY_1=hook.event", "GIT_CONFIG_VALUE_1="}},
		{"round2 A14 two pairs", []string{"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=core.fileMode", "GIT_CONFIG_VALUE_0=false",
			"GIT_CONFIG_KEY_1=core.fileMode", "GIT_CONFIG_VALUE_1=true"},
			[]string{"GIT_CONFIG_COUNT=4", "GIT_CONFIG_KEY_0=core.fileMode", "GIT_CONFIG_VALUE_0=false",
				"GIT_CONFIG_KEY_1=core.fileMode", "GIT_CONFIG_VALUE_1=true",
				"GIT_CONFIG_KEY_2=hook.enabled", "GIT_CONFIG_VALUE_2=false", "GIT_CONFIG_KEY_3=hook.event", "GIT_CONFIG_VALUE_3="}},
		{"round3 P4 Q16 empty value", []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.fileMode", "GIT_CONFIG_VALUE_0="},
			[]string{"GIT_CONFIG_COUNT=3", "GIT_CONFIG_KEY_0=core.fileMode", "GIT_CONFIG_VALUE_0=",
				"GIT_CONFIG_KEY_1=hook.enabled", "GIT_CONFIG_VALUE_1=false", "GIT_CONFIG_KEY_2=hook.event", "GIT_CONFIG_VALUE_2="}},
		{"val2 N4 leading-zero index", []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.fileMode", "GIT_CONFIG_VALUE_0=false",
			"GIT_CONFIG_KEY_01=user.s4", "GIT_CONFIG_VALUE_01=v01"},
			[]string{"GIT_CONFIG_COUNT=3", "GIT_CONFIG_KEY_0=core.fileMode", "GIT_CONFIG_VALUE_0=false",
				"GIT_CONFIG_KEY_1=hook.enabled", "GIT_CONFIG_VALUE_1=false", "GIT_CONFIG_KEY_2=hook.event", "GIT_CONFIG_VALUE_2="}},
	} {
		t.Run(tc.row, func(t *testing.T) {
			clearDaemonConfigEnv(t)
			plantEnv(t, tc.plant...)
			for _, heavy := range []bool{false, true} {
				if got, want := configEntries(hardenedGitEnv(heavy, nil, nil)), sorted(tc.want...); !slices.Equal(got, want) {
					t.Errorf("hardened call (heavy %v) = %q\nwant %q", heavy, got, want)
				}
				// The listing passes the planted set as it is.
				if got, want := configEntries(precursorEnv(heavy, nil)), sorted(tc.plant...); !slices.Equal(got, want) {
					t.Errorf("listing (heavy %v) = %q\nwant %q", heavy, got, want)
				}
			}
		})
	}
}

// Other names that start GIT_CONFIG pass through every call (round1 C2).
func TestHardenedGitEnvKeepsOtherConfigNames(t *testing.T) {
	clearDaemonConfigEnv(t)
	keep := []string{"GIT_CONFIGX=S4MARK_a", "GIT_CONFIG_=S4MARK_b", "GIT_CONFIG_S4=S4MARK_c",
		"GIT_CONFIG_COUNTX=S4MARK_d"}
	plantEnv(t, keep...)
	for _, env := range [][]string{hardenedGitEnv(false, nil, nil), hardenedGitEnv(true, nil, nil), precursorEnv(false, nil)} {
		for _, kv := range keep {
			if !slices.Contains(env, kv) {
				t.Errorf("env lacks %s (round1 C2)", kv)
			}
		}
	}
}

// The count and its pairs are read by their exact upper-case names. On Windows
// os.LookupEnv ignores case, so these rows use the environment list and not the
// process environment.
func TestInheritedConfigProblemExactNames(t *testing.T) {
	for _, tc := range []struct {
		row   string
		env   []string
		count int
		text  string
	}{
		{"windows V8a lower-case count alone", []string{"git_config_count=x"}, 0, ""},
		{"windows V8b lower-case pair", []string{"GIT_CONFIG_COUNT=1", "git_config_key_0=core.fileMode", "git_config_value_0=false"},
			0, "inherited GIT_CONFIG pair 0 is incomplete"},
		{"windows V8u upper-case pair", []string{"GIT_CONFIG_KEY_0=core.fileMode", "GIT_CONFIG_VALUE_0=false", "GIT_CONFIG_COUNT=1"}, 1, ""},
	} {
		t.Run(tc.row, func(t *testing.T) {
			if n, text := inheritedConfigProblem(tc.env); n != tc.count || text != tc.text {
				t.Errorf("inheritedConfigProblem(%q) = %d, %q, want %d, %q", tc.env, n, text, tc.count, tc.text)
			}
		})
	}
}

// A hardened call gets GIT_CONFIG_COUNT, the kept pairs and the hook pins after the
// profile and the GIT_COMMON_DIR pin. The other variables keep the
// daemon's order. A pair name with a leading-zero index is dropped.
func TestHardenedGitEnvOrder(t *testing.T) {
	clearDaemonConfigEnv(t)
	pin := []string{"GIT_COMMON_DIR=/c"}
	head := []string{"HOME=/h", "PATH=/p", "USER=u", "LANG=C", "CLAUDE_SSH_DAEMON_CHILD=1"}
	for _, tc := range []struct {
		row          string
		daemon, tail []string
	}{
		{"round4 R3",
			[]string{"HOME=/h", "PATH=/p", "USER=u", "LANG=C", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=user.s4mark",
				"GIT_CONFIG_VALUE_0=S4KEEP_v", "GIT_CONFIG_KEY_2=S4KEEP_k2", "GIT_CONFIG_VALUE_2=S4KEEP_v2", "CLAUDE_SSH_DAEMON_CHILD=1"},
			[]string{"GIT_COMMON_DIR=/c", "GIT_CONFIG_COUNT=3", "GIT_CONFIG_KEY_0=user.s4mark", "GIT_CONFIG_VALUE_0=S4KEEP_v",
				"GIT_CONFIG_KEY_1=hook.enabled", "GIT_CONFIG_VALUE_1=false", "GIT_CONFIG_KEY_2=hook.event", "GIT_CONFIG_VALUE_2="}},
		{"val2 N4",
			[]string{"HOME=/h", "PATH=/p", "USER=u", "LANG=C", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.fileMode",
				"GIT_CONFIG_VALUE_0=false", "GIT_CONFIG_KEY_01=user.s4", "GIT_CONFIG_VALUE_01=v01", "CLAUDE_SSH_DAEMON_CHILD=1"},
			[]string{"GIT_COMMON_DIR=/c", "GIT_CONFIG_COUNT=3", "GIT_CONFIG_KEY_0=core.fileMode", "GIT_CONFIG_VALUE_0=false",
				"GIT_CONFIG_KEY_1=hook.enabled", "GIT_CONFIG_VALUE_1=false", "GIT_CONFIG_KEY_2=hook.event", "GIT_CONFIG_VALUE_2="}},
	} {
		t.Run(tc.row, func(t *testing.T) {
			light := slices.Concat(head, []string{"GIT_ALLOW_PROTOCOL=https:ssh", "GIT_TERMINAL_PROMPT=0",
				"GIT_NO_REPLACE_OBJECTS=1", "GIT_GRAFT_FILE=" + os.DevNull}, tc.tail)
			heavy := slices.Concat(head, []string{"GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=denied_by_claude_ssh",
				"GIT_ASKPASS=", "GIT_TERMINAL_PROMPT=0"}, tc.tail)
			if got := hardenedEnvFrom(tc.daemon, false, pin, nil); !slices.Equal(got, light) {
				t.Errorf("light hardened env\n got %q\nwant %q", got, light)
			}
			if got := hardenedEnvFrom(tc.daemon, true, pin, nil); !slices.Equal(got, heavy) {
				t.Errorf("heavy hardened env\n got %q\nwant %q", got, heavy)
			}
		})
	}
}

func TestDropConfigOverrides(t *testing.T) {
	// round1 C1 and windows C1w: the exact upper-case names go. git_config and
	// Git_Config_Parameters stay, also on Windows. GIT_CONFIG_GLOBAL stays.
	in := []string{"GIT_CONFIG=S4MARK_1", "git_config=S4MARK_2", "GIT_CONFIG_PARAMETERS=S4MARK_3",
		"Git_Config_Parameters=S4MARK_4", "GIT_CONFIG_GLOBAL=S4MARK_5", "=C:=C:\\", "S4_PLAIN=S4MARK_6"}
	want := []string{"git_config=S4MARK_2", "Git_Config_Parameters=S4MARK_4", "GIT_CONFIG_GLOBAL=S4MARK_5", "=C:=C:\\",
		"S4_PLAIN=S4MARK_6"}
	if got := dropConfigOverrides(in); !slices.Equal(got, want) {
		t.Errorf("dropConfigOverrides = %q\nwant %q", got, want)
	}
}

func TestLightAllowProtocol(t *testing.T) {
	const denied = "denied_by_claude_ssh"
	for _, tc := range []struct{ row, v, want string }{
		{"round1 C4 https:ssh", "https:ssh", "https:ssh"},
		{"round1 C4 empty", "", denied},
		{"round1 C4 file", "file", denied},
		{"round1 C4 https", "https", "https"},
		{"round1 C4 file:ssh", "file:ssh", "ssh"},
		{"round1 C4 ssh:https:git", "ssh:https:git", "ssh:https"},
		{"round2 B1 ssh:ssh", "ssh:ssh", "ssh"},
		{"round2 B2 https::ssh", "https::ssh", "https:ssh"},
		{"round2 B3 :https", ":https", "https"},
		{"round2 B4 HTTPS:SSH", "HTTPS:SSH", denied},
		{"round2 B5 leading space", " https", denied},
		{"round2 B6 trailing space", "https ", denied},
		{"round2 B7 https:ssh:https", "https:ssh:https", "https:ssh"},
		{"round2 B8 git:file", "git:file", denied},
		{"round2 B9 comma", "https,ssh", denied},
		{"macos Xa ssh:file", "ssh:file", "ssh"},
		{"macos Xf file:https:ssh:https", "file:https:ssh:https", "https:ssh"},
		{"macos Xg HTTPS:ssh with a trailing space", "HTTPS:ssh ", denied},
		{"windows AP5 ssh:https", "ssh:https", "ssh:https"},
		{"windows AP6 file:https:git:ssh", "file:https:git:ssh", "https:ssh"},
	} {
		t.Run(tc.row, func(t *testing.T) {
			clearDaemonConfigEnv(t)
			t.Setenv(allowProtocolVar, tc.v)
			if got := lightAllowProtocol(); got != tc.want {
				t.Errorf("lightAllowProtocol() with %q = %q, want %q", tc.v, got, tc.want)
			}
			if !slices.Contains(profileEnv(false), allowProtocolVar+"="+tc.want) {
				t.Errorf("light profile %q lacks %s=%s", profileEnv(false), allowProtocolVar, tc.want)
			}
			if !slices.Contains(profileEnv(true), allowProtocolVar+"="+denied) {
				t.Errorf("heavy profile %q, want %s=%s", profileEnv(true), allowProtocolVar, denied)
			}
		})
	}
	t.Run("round1 C4 unset", func(t *testing.T) {
		clearDaemonConfigEnv(t)
		if got := lightAllowProtocol(); got != "https:ssh" {
			t.Errorf("lightAllowProtocol() unset = %q, want https:ssh", got)
		}
	})
	// claustrum reads the name with os.LookupEnv, which ignores case on Windows.
	// f6010b97 also takes a lower-case git_allow_protocol=ssh there and sends ssh on
	// its light calls (windows V8c).
	t.Run("windows V8c lower-case name", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("names ignore case only on Windows")
		}
		clearDaemonConfigEnv(t)
		t.Setenv("git_allow_protocol", "ssh")
		if got := lightAllowProtocol(); got != "ssh" {
			t.Errorf("lightAllowProtocol() with git_allow_protocol=ssh = %q, want ssh", got)
		}
	})
}

// stubCall is one call that the git-slow stub logged: argv and every GIT_* entry.
type stubCall struct {
	argv []string
	env  []string
}

func readStubCalls(t *testing.T, log string) []stubCall {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var calls []stubCall
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x1e")
		if len(parts) != 5 {
			t.Fatalf("CTXLOG line %q has %d fields, want 5", line, len(parts))
		}
		calls = append(calls, stubCall{argv: strings.Split(parts[2], "\x1f"), env: strings.Split(parts[4], "\x1f")})
	}
	return calls
}

func (c stubCall) value(name string) (string, bool) {
	for _, kv := range c.env {
		if k, v, _ := strings.Cut(kv, "="); k == name {
			return v, true
		}
	}
	return "", false
}

// installEnvStub puts the git-slow stub first on PATH. It returns a function that
// runs one request and returns its frame and the git calls it made.
func installEnvStub(t *testing.T) func(method string, params map[string]any) (string, []stubCall) {
	t.Helper()
	requireGit(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	oldTimeout := gitTimeout
	t.Cleanup(func() { gitTimeout = oldTimeout })
	gitTimeout = 0
	installGitSlowStub(t, realGit)
	resetUserExcludesCache(t)
	resetAttr := func() {
		attrSourceOnce = sync.Once{}
		attrSourceOK = false
	}
	resetAttr()
	t.Cleanup(resetAttr)
	s := newTestServer(t)
	ctxlog := filepath.Join(t.TempDir(), "ctx.log")
	t.Setenv("CLAUSTRUM_GITSTUB_CTXLOG", ctxlog)
	return func(method string, params map[string]any) (string, []stubCall) {
		t.Helper()
		if err := os.WriteFile(ctxlog, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		raw := dispatchRaw(t, s, rpcLine(t, method, params))
		return raw, readStubCalls(t, ctxlog)
	}
}

// TestDaemonGitEnvPerCallKind checks, per call kind, where the daemon's own
// GIT_CONFIG, GIT_CONFIG_PARAMETERS, GIT_CONFIG_COUNT set and GIT_ALLOW_PROTOCOL
// reach git. The rows are round 1 C1, C3 and C4. The excludes read and the
// --attr-source version probe get them as they are. Every listing drops the first
// two and keeps the count set. Every hardened call drops the first two and carries
// the extended count set. The light profile filters GIT_ALLOW_PROTOCOL, and the
// heavy one denies every protocol.
func TestDaemonGitEnvPerCallKind(t *testing.T) {
	run := installEnvStub(t)
	f := newWTFixture(t, false)
	clearDaemonConfigEnv(t)
	cfg := filepath.Join(t.TempDir(), "s4.cfg")
	writeFile(t, cfg, "", 0o644)
	planted := []string{"GIT_CONFIG=" + cfg, "GIT_CONFIG_PARAMETERS='user.s4param'='S4MARK_p'",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=user.s4mark", "GIT_CONFIG_VALUE_0=S4KEEP_v",
		"GIT_CONFIG_KEY_1=S4KEEP_k1", "GIT_CONFIG_KEY_2=S4KEEP_k2", "GIT_CONFIG_VALUE_2=S4KEEP_v2",
		"GIT_ALLOW_PROTOCOL=ssh:https:git"}
	plantEnv(t, planted...)
	listingSet := sorted(planted[2:8]...)
	pinnedSet := sorted("GIT_CONFIG_COUNT=3", "GIT_CONFIG_KEY_0=user.s4mark", "GIT_CONFIG_VALUE_0=S4KEEP_v",
		"GIT_CONFIG_KEY_1=hook.enabled", "GIT_CONFIG_VALUE_1=false", "GIT_CONFIG_KEY_2=hook.event", "GIT_CONFIG_VALUE_2=")

	var calls []stubCall
	for _, r := range []struct {
		method string
		params map[string]any
	}{
		{"git.info", map[string]any{"path": f.top}},
		{"git.worktree_create", map[string]any{"baseRepo": f.top, "branchName": "w1", "worktreePath": f.leaf()}},
		{"git.status", map[string]any{"baseRepo": f.top, "path": f.leaf()}},
	} {
		raw, c := run(r.method, r.params)
		if strings.Contains(raw, `"error"`) || strings.Contains(raw, `"success":false`) {
			t.Fatalf("%s = %s", r.method, raw)
		}
		calls = append(calls, c...)
	}
	kinds := map[string]int{}
	for i, c := range calls {
		got := configEntries(c.env)
		proto, _ := c.value(allowProtocolVar)
		heavy := slices.Contains(c.argv, "core.attributesFile=/dev/null")
		lightProto, heavyProto := "ssh:https", "denied_by_claude_ssh"
		switch {
		case slices.Equal(c.argv, []string{"config", "--includes", "--path", "core.excludesFile"}),
			c.argv[len(c.argv)-1] == "version":
			kinds["unhardened"]++
			if want := sorted(planted[:8]...); !slices.Equal(got, want) {
				t.Errorf("unhardened call %d %q = %q\nwant %q (round1 C1, C3)", i, c.argv, got, want)
			}
			if proto != "ssh:https:git" {
				t.Errorf("unhardened call %d %q GIT_ALLOW_PROTOCOL = %q, want the daemon's (round1 C4)", i, c.argv, proto)
			}
		case slices.Contains(c.argv, "config") && slices.Contains(c.argv, "--list"):
			kinds["listing"]++
			if !slices.Equal(got, listingSet) {
				t.Errorf("listing %d %q = %q\nwant %q (round1 C1, C3)", i, c.argv, got, listingSet)
			}
			if proto != lightProto && proto != heavyProto {
				t.Errorf("listing %d GIT_ALLOW_PROTOCOL = %q, want %s or %s (round1 C4)", i, proto, lightProto, heavyProto)
			}
		default:
			kinds["hardened"]++
			if !slices.Equal(got, pinnedSet) {
				t.Errorf("hardened call %d %q = %q\nwant %q (round1 C1, C3)", i, c.argv, got, pinnedSet)
			}
			want := lightProto
			if heavy {
				want = heavyProto
			}
			if proto != want {
				t.Errorf("hardened call %d %q GIT_ALLOW_PROTOCOL = %q, want %q (round1 C4)", i, c.argv, proto, want)
			}
		}
	}
	// The instrument must see every kind, or a verdict above means nothing.
	if kinds["unhardened"] < 2 || kinds["listing"] == 0 || kinds["hardened"] == 0 {
		t.Fatalf("call kinds seen = %v, want the excludes read, the version probe, listings and hardened calls", kinds)
	}
}

// With a refused count, the excludes read is the one git call of the first
// request. No later request runs git: no listing and no version probe (round 2
// part C, round 3 P1 and P2 on a Linux VM).
func TestInheritedCountRefusalRunsOnlyTheExcludesRead(t *testing.T) {
	run := installEnvStub(t)
	f := newWTFixture(t, false)
	clearDaemonConfigEnv(t)
	t.Setenv(configCountName, "x")
	n := t.TempDir()
	raw, calls := run("git.info", map[string]any{"path": n})
	if !strings.Contains(raw, "is not a count") {
		t.Fatalf("round3 P1 Q1 reply = %s", raw)
	}
	if len(calls) != 1 || !slices.Equal(calls[0].argv, []string{"config", "--includes", "--path", "core.excludesFile"}) {
		t.Fatalf("round3 P1 Q1 calls = %v, want only the excludes read", calls)
	}
	if v, _ := calls[0].value(configCountName); v != "x" {
		t.Errorf("excludes read GIT_CONFIG_COUNT = %q, want x (round2 part C)", v)
	}
	for _, r := range []struct {
		row, method string
		params      map[string]any
	}{
		{"round1 B6", "git.info", map[string]any{"path": f.top}},
		{"round1 B6", "git.list_branches", map[string]any{"path": f.top}},
		{"round3 Q7", "git.status", map[string]any{"baseRepo": f.top, "path": f.top}},
		{"round1 B6", "git.worktree_create", map[string]any{"baseRepo": f.top, "branchName": "w1", "worktreePath": f.leaf()}},
		{"round3 Q14", "git.worktree_remove", map[string]any{"baseRepo": f.top, "worktreePath": f.leaf()}},
	} {
		raw, calls := run(r.method, r.params)
		if !strings.Contains(raw, "is not a count") {
			t.Errorf("%s %s = %s, want the count refusal", r.row, r.method, raw)
		}
		if len(calls) != 0 {
			t.Errorf("round2 part C %s ran git: %v", r.method, calls)
		}
	}
}

// TestInheritedCountRefusalPlacement runs the round 3 rows through dispatch. With a
// refused count, these rows refuse, except git.list_branches on a missing dir. P0 is
// the control with nothing planted.
func TestInheritedCountRefusalPlacement(t *testing.T) {
	f := newWTFixture(t, false)
	resetUserExcludesCache(t)
	base := t.TempDir()
	requireTempOutsideCheckout(t, base)
	n := filepath.Join(base, "N")
	writeFile(t, filepath.Join(n, "n.txt"), "n\n", 0o644)
	missing := filepath.Join(base, "missing")
	s := newTestServer(t)
	oldTimeout := gitTimeout
	t.Cleanup(func() { gitTimeout = oldTimeout })
	gitTimeout = 0

	const pre = "config-defined hooks could not be pinned off; git not run: "
	errFrame := func(msg string) string {
		return `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":` + jsonString(t, msg) + `}}`
	}
	addFrame := func(msg string) string {
		return `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + jsonString(t, msg) + `,"errorCode":"worktree_add_failed"}}`
	}
	lockFrame := func(path, reason string) string {
		return `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` +
			jsonString(t, "failed to remove worktree: could not check whether "+path+" is locked ("+reason+"); retry") + `}}`
	}
	type row struct {
		name, method string
		params       map[string]any
		p0           string // the P0 frame
		shape        func(msg string) string
	}
	nLeaf := filepath.Join(n, ".claude", "worktrees", "w1")
	missLeaf := filepath.Join(missing, ".claude", "worktrees", "w1")
	nope := filepath.Join(f.top, ".claude", "worktrees", "nope")
	rows := []row{
		{"Q1 git.info N", "git.info", map[string]any{"path": n},
			`{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"repoSlug":"","defaultBranch":""}}`, errFrame},
		{"Q2 git.info missing", "git.info", map[string]any{"path": missing},
			`{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"repoSlug":"","defaultBranch":""}}`, errFrame},
		// With nothing planted, f6010b97 on Windows answers Q3 with the start error of
		// its listing (Windows VM). This row checks claustrum's frame there.
		{"Q3 git.info regular file", "git.info", map[string]any{"path": filepath.Join(n, "n.txt")},
			`{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"repoSlug":"","defaultBranch":""}}`, errFrame},
		{"Q4 list_branches N", "git.list_branches", map[string]any{"path": n},
			`{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"branches":[]}}`, errFrame},
		{"Q5 list_branches missing", "git.list_branches", map[string]any{"path": missing},
			`{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"branches":[]}}`, nil},
		{"Q6 status N base T", "git.status", map[string]any{"path": n, "baseRepo": f.top},
			`{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"clean":false}}`, errFrame},
		{"Q7 status T base T", "git.status", map[string]any{"path": f.top, "baseRepo": f.top},
			`{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"clean":false}}`, errFrame},
		{"Q8 status missing base T", "git.status", map[string]any{"path": missing, "baseRepo": f.top},
			`{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"clean":false}}`, errFrame},
		{"Q9 create base N", "git.worktree_create", map[string]any{"baseRepo": n, "worktreePath": nLeaf, "branchName": "w1"},
			`{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"not a git repository","errorCode":"not_a_repo"}}`, addFrame},
		{"Q10 create base missing", "git.worktree_create",
			map[string]any{"baseRepo": missing, "worktreePath": filepath.Join(missing, "w1"), "branchName": "w1"},
			`{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"not a git repository","errorCode":"not_a_repo"}}`, addFrame},
		{"Q11 create relative path", "git.worktree_create", map[string]any{"baseRepo": f.top, "worktreePath": "rel/w1", "branchName": "w1"},
			`{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"refusing to create worktree: rel/w1 is a relative path; choose the session folder by its absolute path, without \"..\"","errorCode":"unsafe_path"}}`,
			addFrame},
		{"Q12 remove base N", "git.worktree_remove", map[string]any{"baseRepo": n, "worktreePath": nLeaf},
			`{"jsonrpc":"2.0","id":1,"result":{"success":true}}`, func(m string) string { return lockFrame(nLeaf, m) }},
		{"Q13 remove base missing", "git.worktree_remove", map[string]any{"baseRepo": missing, "worktreePath": missLeaf},
			`{"jsonrpc":"2.0","id":1,"result":{"success":true}}`, func(m string) string { return lockFrame(missLeaf, m) }},
		{"Q14 remove unregistered leaf", "git.worktree_remove", map[string]any{"baseRepo": f.top, "worktreePath": nope},
			`{"jsonrpc":"2.0","id":1,"result":{"success":true}}`, func(m string) string { return lockFrame(nope, m) }},
	}
	for _, phase := range []struct {
		name  string
		plant []string
		text  string
	}{
		{"P0 control", nil, ""},
		{"P1 letter", []string{"GIT_CONFIG_COUNT=x"}, `inherited GIT_CONFIG_COUNT "x" is not a count`},
		{"P2 pair 1 missing", []string{"GIT_CONFIG_KEY_0=core.fileMode", "GIT_CONFIG_VALUE_0=false", "GIT_CONFIG_COUNT=2"},
			"inherited GIT_CONFIG pair 1 is incomplete"},
	} {
		t.Run(phase.name, func(t *testing.T) {
			clearDaemonConfigEnv(t)
			plantEnv(t, phase.plant...)
			for _, r := range rows {
				want := r.p0
				if phase.text != "" && r.shape != nil {
					want = r.shape(pre + phase.text)
				}
				if got := dispatchRaw(t, s, rpcLine(t, r.method, r.params)); got != want {
					t.Errorf("round3 %s %s\n got %s\nwant %s", phase.name, r.name, got, want)
				}
			}
		})
	}
}

// A worktree directory that is still there gets the lock-check frame without the
// reason (validation V3, a plain dir). With a worktreeRoot the reason follows
// "cannot determine the repository's work tree" (round1 B6 M8). On Windows the
// worktreeRoot refusal comes first (windows B6 M8).
func TestInheritedCountRefusalOnRemove(t *testing.T) {
	f := newWTFixture(t, false)
	resetUserExcludesCache(t)
	s := newTestServer(t)
	oldTimeout := gitTimeout
	t.Cleanup(func() { gitTimeout = oldTimeout })
	gitTimeout = 0
	clearDaemonConfigEnv(t)
	t.Setenv(configCountName, "x")
	const refusal = `config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG_COUNT "x" is not a count`

	t.Run("validation V3 present leaf", func(t *testing.T) {
		if err := os.MkdirAll(f.leaf(), 0o755); err != nil {
			t.Fatal(err)
		}
		want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` +
			jsonString(t, "failed to remove worktree: could not check whether "+f.leaf()+
				" is locked (its registrations could not be examined); retry") + `}}`
		got := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove", map[string]any{"baseRepo": f.top, "worktreePath": f.leaf()}))
		if got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	})
	t.Run("round1 B6 M8 worktreeRoot", func(t *testing.T) {
		root := t.TempDir()
		requireTempOutsideCheckout(t, root)
		ext := filepath.Join(root, "cp", "w2")
		text := "failed to remove worktree: cannot determine the repository's work tree: " + refusal
		if msg := externalWorktreeUnsupportedRefusal(root, "remove"); msg != "" {
			text = msg
		}
		want := `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + jsonString(t, text) + `}}`
		got := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove",
			map[string]any{"baseRepo": f.top, "worktreePath": ext, "worktreeRoot": root}))
		if got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	})
}

// TestInheritedCountRefusalRound4 runs the round 4 rows and the val2 N2 rows through
// dispatch, with nothing planted (P0) and with GIT_CONFIG_COUNT=x (P1). On Windows a
// row with a worktreeRoot answers the worktreeRoot refusal in both phases (windows
// R1d to R1g, N2a, N2b).
func TestInheritedCountRefusalRound4(t *testing.T) {
	f := newWTFixture(t, false)
	resetUserExcludesCache(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	requireTempOutsideCheckout(t, base)
	n := filepath.Join(base, "N")
	writeFile(t, filepath.Join(n, "n.txt"), "n\n", 0o644)
	missing := filepath.Join(base, "missing")
	root := filepath.Join(base, "R")
	w9 := filepath.Join(root, "cp", "w9")
	x := filepath.Join(base, "X")
	y := filepath.Join(base, "Y")
	writeFile(t, filepath.Join(y, "y.txt"), "y\n", 0o644)
	if err := os.MkdirAll(x, 0o755); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t)
	oldTimeout := gitTimeout
	t.Cleanup(func() { gitTimeout = oldTimeout })
	gitTimeout = 0

	const refusal = `config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG_COUNT "x" is not a count`
	removeFrame := func(msg string) string {
		return `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + jsonString(t, msg) + `}}`
	}
	sep := string(filepath.Separator)
	dotdot := f.top + sep + ".claude" + sep + "worktrees" + sep + ".." + sep + ".." + sep + "x"
	// leaf is the state of R/cp/w9 before a request.
	const (
		leafAbsent = iota
		leafEmpty
		leafContent // R/cp/w9/w9.txt exists
	)
	type row struct {
		name, method string
		params       map[string]any
		p0, p1       string
		leaf         int
		goneP0       bool // the P0 request deletes the leaf
	}
	success := `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`
	statusBare := `{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"clean":false}}`
	statusRefused := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":` + jsonString(t, refusal) + `}}`
	relative := removeFrame(`refusing to remove worktree: rel/w1 is a relative path; choose the session folder by its absolute path, without ".."`)
	outside := func(p string) string {
		return removeFrame("refusing to remove worktree: " + p + " is not inside the repository " + f.top +
			"; session worktrees are only created and removed under <repository>/.claude/worktrees")
	}
	notWorktree := removeFrame("refusing to remove worktree: " + w9 + " is not a worktree of " + missing + " (" + w9 +
		" has no .git file), so it is left in place; remove it by hand if it is a leftover")
	unreadRepo := removeFrame("failed to remove worktree: could not check whether " + w9 +
		" is locked (the repository at " + missing + " could not be read); retry")
	workTree := func(reason string) string {
		return removeFrame("failed to remove worktree: cannot determine the repository's work tree: " + reason)
	}
	beneath := removeFrame("refusing to remove worktree: " + y + " is not <worktree location>/<directory>/<name> beneath " + root)
	dotdotFrame := removeFrame("refusing to remove worktree: " + dotdot +
		` contains a ".." component; choose the session folder by its absolute path, without ".."`)
	external := func(base, wt string) map[string]any {
		return map[string]any{"baseRepo": base, "worktreePath": wt, "worktreeRoot": root}
	}
	rows := []row{
		{"round4 R1a relative", "git.worktree_remove", map[string]any{"baseRepo": f.top, "worktreePath": "rel/w1"},
			relative, relative, leafAbsent, false},
		{"round4 R1b dir outside T", "git.worktree_remove", map[string]any{"baseRepo": f.top, "worktreePath": x},
			outside(x), outside(x), leafAbsent, false},
		{"round4 R1c T itself", "git.worktree_remove", map[string]any{"baseRepo": f.top, "worktreePath": f.top},
			outside(f.top), outside(f.top), leafAbsent, false},
		{"round4 R1d missing baseRepo", "git.worktree_remove", external(missing, w9), notWorktree, unreadRepo, leafContent, false},
		{"round4 R1e plain-dir baseRepo", "git.worktree_remove", external(n, w9),
			workTree("exit status 128"), workTree(refusal), leafContent, false},
		{"round4 R1f leaf missing", "git.worktree_remove", external(f.top, w9), success, workTree(refusal), leafAbsent, false},
		{"round4 R1g outside R", "git.worktree_remove", external(f.top, y), beneath, beneath, leafContent, false},
		{"round4 R1h dot-dot", "git.worktree_remove", map[string]any{"baseRepo": f.top, "worktreePath": dotdot},
			dotdotFrame, dotdotFrame, leafAbsent, false},
		{"val2 N2a missing baseRepo, empty leaf", "git.worktree_remove", external(missing, w9), success, unreadRepo, leafEmpty, true},
		{"val2 N2b missing baseRepo, leaf absent", "git.worktree_remove", external(missing, w9), success,
			removeFrame("failed to remove worktree: could not check whether " + w9 + " is locked (" + refusal + "); retry"),
			leafAbsent, false},
		{"round4 R2a plain-dir baseRepo", "git.status", map[string]any{"baseRepo": n, "path": n}, statusBare, statusRefused, leafAbsent, false},
		{"round4 R2b missing baseRepo", "git.status", map[string]any{"baseRepo": missing, "path": missing}, statusBare, statusBare, leafAbsent, false},
		{"round4 R2c plain-dir baseRepo, path T", "git.status", map[string]any{"baseRepo": n, "path": f.top},
			statusBare, statusRefused, leafAbsent, false},
		{"round4 R2d regular-file path", "git.status", map[string]any{"baseRepo": f.top, "path": filepath.Join(f.top, "t.txt")},
			statusBare, statusRefused, leafAbsent, false},
	}
	writeFile(t, filepath.Join(f.top, "x", "x.txt"), "x\n", 0o644)
	if err := os.MkdirAll(filepath.Join(f.top, ".claude", "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []struct {
		name  string
		plant []string
	}{
		{"P0 control", nil},
		{"P1 letter", []string{"GIT_CONFIG_COUNT=x"}},
	} {
		t.Run(phase.name, func(t *testing.T) {
			clearDaemonConfigEnv(t)
			plantEnv(t, phase.plant...)
			for _, r := range rows {
				if err := os.RemoveAll(root); err != nil {
					t.Fatal(err)
				}
				// The VM runs made R for every row. Without R, some rows get the "not
				// reachable" refusal instead.
				if err := os.MkdirAll(root, 0o755); err != nil {
					t.Fatal(err)
				}
				switch r.leaf {
				case leafEmpty:
					if err := os.MkdirAll(w9, 0o755); err != nil {
						t.Fatal(err)
					}
				case leafContent:
					writeFile(t, filepath.Join(w9, "w9.txt"), "w9\n", 0o644)
				}
				want, gone := r.p0, r.goneP0
				if phase.plant != nil {
					want, gone = r.p1, false
				}
				if wr, ok := r.params["worktreeRoot"].(string); ok {
					if msg := externalWorktreeUnsupportedRefusal(wr, "remove"); msg != "" {
						want, gone = removeFrame(msg), false
					}
				}
				if got := dispatchRaw(t, s, rpcLine(t, r.method, r.params)); got != want {
					t.Errorf("%s %s\n got %s\nwant %s", phase.name, r.name, got, want)
				}
				if r.leaf == leafAbsent {
					continue
				}
				if _, err := os.Stat(w9); (err == nil) == gone {
					t.Errorf("%s %s: leaf present %v, want %v", phase.name, r.name, err == nil, !gone)
				}
				if r.leaf == leafContent {
					if _, err := os.Stat(filepath.Join(w9, "w9.txt")); err != nil {
						t.Errorf("%s %s deleted the leaf content: %v", phase.name, r.name, err)
					}
				}
			}
		})
	}
}

// git.status answers a baseRepo that does not resolve with the bare shape, with no
// git call, with nothing planted and with a refused count (val2 N1, X1, X1b and the
// val3 S1 rows). In every row but N1, path is a worktree of T. The control rows are
// spellings of T that resolve.
func TestStatusUnresolvableBaseRepo(t *testing.T) {
	f := newWTFixture(t, false)
	b := filepath.Dir(f.top)
	requireTempOutsideCheckout(t, b)
	w1 := f.leaf()
	runGit(t, f.top, "worktree", "add", "-q", "-b", "w1", w1)
	run := installEnvStub(t)
	sep := string(filepath.Separator)
	join := func(parts ...string) string { return strings.Join(parts, sep) }
	// link makes a symbolic link. On Windows a failure gives the reason to skip the row.
	link := func(target, name string) string {
		t.Helper()
		if err := os.Symlink(target, name); err != nil {
			if runtime.GOOS != "windows" {
				t.Fatal(err)
			}
			return "symlink: " + err.Error()
		}
		return ""
	}
	if err := os.MkdirAll(filepath.Join(b, "A", "B"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(b, "F"), "f\n", 0o644)
	skipDL := link(filepath.Join(b, "gone-dl"), filepath.Join(f.top, "dl"))
	skipD := link(filepath.Join(b, "gone-D"), filepath.Join(b, "D"))
	skipL := link(filepath.Join(b, "A", "B"), filepath.Join(b, "L"))
	skipLoop := link(filepath.Join(b, "loop"), filepath.Join(b, "loop"))
	skipNA := ""
	switch {
	case runtime.GOOS == "windows":
		skipNA = "mode 000 does not deny access on Windows"
	case os.Geteuid() == 0:
		skipNA = "root ignores mode 000"
	default:
		na := filepath.Join(b, "na")
		if err := os.MkdirAll(filepath.Join(na, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(na, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(na, 0o755) })
	}
	rows := []struct{ row, base, path, skip string }{
		{"val2 N1", filepath.Join(b, "missing"), f.top, ""},
		{"val2 X1", join(f.top, "missing", ".."), w1, ""},
		{"val2 X1b", filepath.Join(f.top, "missing"), w1, ""},
		{"val3 S1a dangling link then ..", join(f.top, "dl", ".."), w1, skipDL},
		{"val3 S1b", join(b, "D", "..", "T"), w1, skipD},
		{"val3 S1c live link then ..", join(b, "L", "..", "T"), w1, skipL},
		{"val3 S1d", join(b, "missing", "..", "T"), w1, ""},
		{"val3 S1e", join(f.top, "missing", "sub", "..", ".."), w1, ""},
		{"val3 S1f regular file then ..", join(b, "F", "..", "T"), w1, ""},
		{"val3 S1g loop", join(b, "loop", "..", "T"), w1, skipLoop},
		{"val3 S1h mode 000", join(b, "na", "sub", "..", "..", "T"), w1, skipNA},
		{"val3 windows S1j tracked file then ..", join(f.top, "t.txt", ".."), w1, ""},
	}
	controls := []struct{ row, base string }{
		{"control T", f.top},
		{"control T/", f.top + sep},
		{"control T/.", join(f.top, ".")},
		{"control T/.claude/..", join(f.top, ".claude", "..")},
	}
	const bare = `{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"clean":false}}`
	for _, phase := range []struct {
		name, control string
		plant         []string
	}{
		{"P0 control", `{"jsonrpc":"2.0","id":1,"result":{"isRepo":true,"clean":true}}`, nil},
		{"P1 letter", `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":` +
			jsonString(t, `config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG_COUNT "x" is not a count`) + `}}`,
			[]string{"GIT_CONFIG_COUNT=x"}},
	} {
		t.Run(phase.name, func(t *testing.T) {
			clearDaemonConfigEnv(t)
			plantEnv(t, phase.plant...)
			for _, r := range rows {
				if r.skip != "" {
					t.Logf("%s skipped: %s", r.row, r.skip)
					continue
				}
				raw, calls := run("git.status", map[string]any{"baseRepo": r.base, "path": r.path})
				if raw != bare {
					t.Errorf("%s\n got %s\nwant %s", r.row, raw, bare)
				}
				if len(calls) != 0 {
					t.Errorf("%s ran git: %v", r.row, calls)
				}
			}
			for _, c := range controls {
				if raw, _ := run("git.status", map[string]any{"baseRepo": c.base, "path": w1}); raw != phase.control {
					t.Errorf("%s\n got %s\nwant %s", c.row, raw, phase.control)
				}
			}
		})
	}
}

// git.list_branches answers a path inside a managed worktrees directory, and a
// non-empty path that does not resolve, with the bare shape. No git call runs, with
// nothing planted and with a refused count. The rows are val3 Z1a (a linked
// worktree under .claude/worktrees), Z1b (a repository there), Z1c (a repository
// below a directory holding the marker), Z1c-w9 (a worktreeRoot worktree), val3
// windows Z2 (a missing component before "..") and val4 L1f (a regular file before
// "..").
func TestListBranchesManagedOrUnresolvablePath(t *testing.T) {
	f := newWTFixture(t, false)
	b := filepath.Dir(f.top)
	requireTempOutsideCheckout(t, b)
	worktrees := filepath.Join(f.top, ".claude", "worktrees")
	lw := filepath.Join(worktrees, "lw")
	runGit(t, f.top, "worktree", "add", "-q", "-b", "lw", lw)
	inner := filepath.Join(worktrees, "inner")
	initTrustMain(t, inner)
	marked := filepath.Join(b, "M")
	writeFile(t, filepath.Join(marked, managedWorktreesMarker), "", 0o644)
	held := filepath.Join(marked, "T2")
	initTrustMain(t, held)
	writeFile(t, filepath.Join(b, "F"), "f\n", 0o644)
	run := installEnvStub(t)
	clearDaemonConfigEnv(t)
	sep := string(filepath.Separator)
	rows := []struct{ row, path string }{
		{"val3 Z1a", lw},
		{"val3 Z1b", inner},
		{"val3 Z1c", held},
		{"val3 windows Z2", f.top + sep + "missing" + sep + ".."},
		{"val4 L1f", strings.Join([]string{b, "F", "..", "T"}, sep)},
	}
	branches := `["ex","lw","main"]`
	// Windows refuses a worktreeRoot, so the Z1c-w9 row runs on Linux and macOS.
	root := filepath.Join(b, "R")
	if externalWorktreeUnsupportedRefusal(root, "create") == "" {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		w9 := filepath.Join(root, "cp", "w9")
		raw, _ := run("git.worktree_create", map[string]any{"baseRepo": f.top, "worktreePath": w9,
			"worktreeRoot": root, "branchName": "w9"})
		if !strings.Contains(raw, `"success":true`) {
			t.Fatalf("create of w9 = %s", raw)
		}
		rows = append(rows, struct{ row, path string }{"val3 Z1c-w9", w9})
		branches = `["ex","lw","main","w9"]`
	}
	const bare = `{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"branches":[]}}`
	for _, phase := range []struct {
		name, control string
		plant         []string
	}{
		{"P0 control", `{"jsonrpc":"2.0","id":1,"result":{"isRepo":true,"branches":` + branches + `}}`, nil},
		{"P1 letter", `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":` +
			jsonString(t, `config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG_COUNT "x" is not a count`) + `}}`,
			[]string{"GIT_CONFIG_COUNT=x"}},
	} {
		t.Run(phase.name, func(t *testing.T) {
			clearDaemonConfigEnv(t)
			plantEnv(t, phase.plant...)
			if raw, _ := run("git.list_branches", map[string]any{"path": f.top}); raw != phase.control {
				t.Errorf("val3 Z1-T\n got %s\nwant %s", raw, phase.control)
			}
			for _, r := range rows {
				raw, calls := run("git.list_branches", map[string]any{"path": r.path})
				if raw != bare {
					t.Errorf("%s\n got %s\nwant %s", r.row, raw, bare)
				}
				if len(calls) != 0 {
					t.Errorf("%s ran git: %v", r.row, calls)
				}
			}
		})
	}
}

// git.list_branches with an empty or absent path refuses under a refused count, with
// the daemon's working directory in a repository (val2 X2) or in a plain dir (val2
// X2c). No git call runs other than the excludes read.
func TestListBranchesEmptyPathCountRefusal(t *testing.T) {
	f := newWTFixture(t, false)
	n := t.TempDir()
	requireTempOutsideCheckout(t, n)
	run := installEnvStub(t)
	refused := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":` +
		jsonString(t, `config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG_COUNT "x" is not a count`) + `}}`
	for _, cwd := range []struct {
		row, dir, p0 string
	}{
		{"val2 X2", f.top, `{"jsonrpc":"2.0","id":1,"result":{"isRepo":true,"branches":["ex","main"]}}`},
		{"val2 X2c", n, `{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"branches":[]}}`},
	} {
		t.Run(cwd.row, func(t *testing.T) {
			t.Chdir(cwd.dir)
			for _, phase := range []struct {
				name  string
				plant []string
				want  string
			}{
				{"P0 control", nil, cwd.p0},
				{"P1 letter", []string{"GIT_CONFIG_COUNT=x"}, refused},
			} {
				clearDaemonConfigEnv(t)
				plantEnv(t, phase.plant...)
				for _, params := range []map[string]any{{"path": ""}, {}} {
					raw, calls := run("git.list_branches", params)
					if raw != phase.want {
						t.Errorf("%s %v\n got %s\nwant %s", phase.name, params, raw, phase.want)
					}
					if phase.plant == nil {
						continue
					}
					for _, c := range calls {
						if !slices.Equal(c.argv, []string{"config", "--includes", "--path", "core.excludesFile"}) {
							t.Errorf("%s %v ran %q", phase.name, params, c.argv)
						}
					}
				}
			}
		})
	}
}

// A trust refusal wins over the count refusal. The rows are val2 Y1 with a stray
// commondir in T and val2 N3c with an entry whose commondir names another
// repository (M2). The val3 Z3 rows remove R/cp/w9 with worktreeRoot R and baseRepo
// the Y1 repository: Z3a with the leaf absent, Z3a2 with R/cp present, Z3b with an
// empty leaf and Z3c with a file in the leaf. Nothing is deleted. On Windows the
// worktreeRoot refusal comes first. P0 is the control with nothing planted.
func TestTrustRefusalBeforeCountRefusal(t *testing.T) {
	stray := newTrustRepo(t)
	requireTempOutsideCheckout(t, stray.base)
	strayCD := filepath.Join(stray.T, ".git", "commondir")
	writeFile(t, strayCD, "x\n", 0o644)
	m1 := wantTR(strayCD, "x")
	foreign := newTrustRepo(t)
	other := filepath.Join(foreign.base, "X")
	initTrustMain(t, other)
	foreignCD := filepath.Join(foreign.entry(), "commondir")
	writeFile(t, foreignCD, filepath.Join(other, ".git")+"\n", 0o644)
	m2 := wantM2(foreignCD, filepath.Join(foreign.T, ".git"))
	resetUserExcludesCache(t)
	lockCheck := func(wt, reason string) string {
		b, err := json.Marshal(worktreeRemoveResult{Success: false,
			Error: "failed to remove worktree: could not check whether " + wt + " is locked (" + reason + "); retry"})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	strayGone := filepath.Join(stray.T, ".claude", "worktrees", "gone")
	foreignGone := filepath.Join(foreign.TW, ".claude", "worktrees", "gone")
	root := filepath.Join(stray.base, "R")
	w9 := filepath.Join(root, "cp", "w9")
	for _, phase := range []struct {
		name  string
		plant []string
	}{
		{"P0 control", nil},
		{"P1 letter", []string{"GIT_CONFIG_COUNT=x"}},
	} {
		t.Run(phase.name, func(t *testing.T) {
			clearDaemonConfigEnv(t)
			plantEnv(t, phase.plant...)
			wantRPCError(t, "val2 Y1 git.info", info(t, stray.T), m1)
			wantRPCError(t, "val2 Y1 git.status", status(t, stray.SW, stray.T), m1)
			wantResult(t, "val2 Y1 git.worktree_remove", remove(t, stray.T, strayGone, ""), lockCheck(strayGone, m1))
			wantRPCError(t, "val2 N3c git.info", info(t, foreign.TW), m2)
			wantRPCError(t, "val2 N3c git.list_branches", listBranches(t, foreign.TW), m2)
			wantRPCError(t, "val2 N3c git.status", status(t, foreign.TW, foreign.TW), m2)
			wantCreateRefused(t, foreign.TW, foreign.T, m2)
			wantResult(t, "val2 N3c git.worktree_remove", remove(t, foreign.TW, foreignGone, ""), lockCheck(foreignGone, m2))
			for _, r := range []struct {
				row  string
				make string // what exists below R before the request
			}{{"val3 Z3a", ""}, {"val3 Z3a2", "cp"}, {"val3 Z3b", "leaf"}, {"val3 Z3c", "file"}} {
				if err := os.RemoveAll(root); err != nil {
					t.Fatal(err)
				}
				mk := map[string]string{"": root, "cp": filepath.Dir(w9), "leaf": w9, "file": w9}[r.make]
				if err := os.MkdirAll(mk, 0o755); err != nil {
					t.Fatal(err)
				}
				if r.make == "file" {
					writeFile(t, filepath.Join(w9, "f.txt"), "f\n", 0o644)
				}
				text := "failed to remove worktree: cannot determine the repository's work tree: " + m1
				if msg := externalWorktreeUnsupportedRefusal(root, "remove"); msg != "" {
					text = msg
				}
				b, err := json.Marshal(worktreeRemoveResult{Success: false, Error: text})
				if err != nil {
					t.Fatal(err)
				}
				wantResult(t, r.row+" git.worktree_remove", trustCall(t, "git.worktree_remove",
					map[string]any{"baseRepo": stray.T, "worktreePath": w9, "worktreeRoot": root}), string(b))
				if r.make == "leaf" && !exists(w9) {
					t.Errorf("%s deleted the leaf", r.row)
				}
				if r.make == "file" && !exists(filepath.Join(w9, "f.txt")) {
					t.Errorf("%s deleted the leaf content", r.row)
				}
			}
		})
	}
}

// git.worktree_create checks a baseRepo inside a managed worktrees directory before
// the count (val2 Y3d an independent repository, val2 Y3e a linked worktree). The
// "..", outside and already-exists refusals come after the count (val2 Y3a to Y3c).
func TestCreateNestedBaseBeforeCountRefusal(t *testing.T) {
	f := newWTFixture(t, false)
	worktrees := filepath.Join(f.top, ".claude", "worktrees")
	inner := filepath.Join(worktrees, "inner")
	initTrustMain(t, inner)
	lw := filepath.Join(worktrees, "lw")
	runGit(t, f.top, "worktree", "add", "-q", "-b", "lw", lw)
	writeFile(t, filepath.Join(f.leaf(), "a.txt"), "a\n", 0o644)
	resetUserExcludesCache(t)
	s := newTestServer(t)
	sep := string(filepath.Separator)
	dotdot := f.top + sep + ".claude" + sep + "worktrees" + sep + ".." + sep + ".." + sep + "x"
	outside := filepath.Join(filepath.Dir(f.top), "X", "w1")
	frame := func(msg, code string) string {
		b, err := json.Marshal(worktreeResult{Success: false, Error: msg, ErrorCode: code})
		if err != nil {
			t.Fatal(err)
		}
		return `{"jsonrpc":"2.0","id":1,"result":` + string(b) + `}`
	}
	nested := frame(managedWorktreesRefusal, "nested_base_repo")
	counted := frame(`config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG_COUNT "x" is not a count`,
		"worktree_add_failed")
	rows := []struct {
		row      string
		base, wt string
		p0, p1   string
	}{
		{"val2 Y3a", f.top, dotdot, frame("refusing to create worktree: "+dotdot+
			` contains a ".." component; choose the session folder by its absolute path, without ".."`, "unsafe_path"), counted},
		{"val2 Y3b", f.top, outside, frame("refusing to create worktree: "+outside+" is not inside the repository "+f.top+
			"; session worktrees are only created and removed under <repository>/.claude/worktrees", "unsafe_path"), counted},
		{"val2 Y3c", f.top, f.leaf(), frame("refusing to create worktree: "+f.leaf()+
			" already exists, and a new worktree is only ever created in a fresh directory", "unsafe_path"), counted},
		{"val2 Y3d", inner, filepath.Join(inner, ".claude", "worktrees", "w1"), nested, nested},
		{"val2 Y3e", lw, filepath.Join(lw, ".claude", "worktrees", "w1"), nested, nested},
	}
	for _, phase := range []struct {
		name  string
		plant []string
	}{
		{"P0 control", nil},
		{"P1 letter", []string{"GIT_CONFIG_COUNT=x"}},
	} {
		t.Run(phase.name, func(t *testing.T) {
			clearDaemonConfigEnv(t)
			plantEnv(t, phase.plant...)
			for _, r := range rows {
				want := r.p0
				if phase.plant != nil {
					want = r.p1
				}
				got := dispatchRaw(t, s, rpcLine(t, "git.worktree_create",
					map[string]any{"baseRepo": r.base, "worktreePath": r.wt, "branchName": "y3"}))
				if got != want {
					t.Errorf("%s\n got %s\nwant %s", r.row, got, want)
				}
			}
		})
	}
}
