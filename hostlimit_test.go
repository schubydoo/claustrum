package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/schubydoo/claustrum/internal/rpc"
)

const hostLimitTestPath = "/etc/claude-desktop-ssh/settings.json"

// TestJudgeHostLimit: the limit, the clamp and the fault of each measured body of the host
// settings file (13047a51, measured on Linux).
func TestJudgeHostLimit(t *testing.T) {
	pad := `{"unattendedLimitMinutes": 30, "p": "%s"}`
	for _, tc := range []struct {
		name    string
		body    string
		minutes int
		asked   string
		fault   string
	}{
		{"v1", `{"unattendedLimitMinutes": 1}`, 15, "1", ""},
		{"v14", `{"unattendedLimitMinutes": 14}`, 15, "14", ""},
		{"v15", `{"unattendedLimitMinutes": 15}`, 15, "", ""},
		{"v60", `{"unattendedLimitMinutes": 60}`, 60, "", ""},
		{"v43200", `{"unattendedLimitMinutes": 43200}`, 43200, "", ""},
		{"v43201", `{"unattendedLimitMinutes": 43201}`, 43200, "43201", ""},
		{"vbig", `{"unattendedLimitMinutes": 1000000000000}`, 43200, "1000000000000", ""},
		{"v0", `{"unattendedLimitMinutes": 0}`, 0, "", hostFaultValue},
		{"vneg", `{"unattendedLimitMinutes": -1}`, 0, "", hostFaultValue},
		{"vfloat", `{"unattendedLimitMinutes": 1.5}`, 0, "", hostFaultValue},
		{"vstr", `{"unattendedLimitMinutes": "60"}`, 0, "", hostFaultValue},
		{"vtrue", `{"unattendedLimitMinutes": true}`, 0, "", hostFaultValue},
		{"vnull", `{"unattendedLimitMinutes": null}`, 0, "", hostFaultValue},
		{"empty_obj", `{}`, 0, "", ""},
		{"unknown", `{"unattendedLimitMinutes": 30, "other": 1, "x": {"y": 2}}`, 30, "", ""},
		{"bom", "\xef\xbb\xbf" + `{"unattendedLimitMinutes": 30}`, 30, "", ""},
		{"limit_nl", `{"unattendedLimitMinutes": 30}` + "\n", 30, "", ""},
		{"size64k", strings.Replace(pad, "%s", strings.Repeat("a", hostLimitMaxSize-len(pad)+2), 1), 30, "", ""},
		{"badjson", `{`, 0, "", hostFaultSyntax},
		{"empty", ``, 0, "", hostFaultSyntax},
		{"array", `[1]`, 0, "", hostFaultSyntax},
		{"scalar", `5`, 0, "", hostFaultSyntax},
		{"trailing", `{"unattendedLimitMinutes": 30} x`, 0, "", hostFaultSyntax},
		{"twoobj", `{"unattendedLimitMinutes": 30}{"a":1}`, 0, "", hostFaultSyntax},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := judgeHostLimit(hostLimitTestPath, []byte(tc.body))
			if h.minutes != tc.minutes || h.asked != tc.asked || h.fault != tc.fault {
				t.Errorf("got minutes %d, asked %q, fault %q, want %d, %q, %q", h.minutes, h.asked, h.fault, tc.minutes, tc.asked, tc.fault)
			}
		})
	}
}

// TestHostLimitStartLines: the start line of each kind, byte for byte as 13047a51 logs it
// after its time (measured on Linux). claustrum puts its level tag first.
func TestHostLimitStartLines(t *testing.T) {
	for _, tc := range []struct {
		body string
		want string
	}{
		{`{}`, "INFO  [Server] No unattended limit is set in /etc/claude-desktop-ssh/settings.json\n"},
		{`{"unattendedLimitMinutes": 1}`, "INFO  [Server] /etc/claude-desktop-ssh/settings.json sets an unattended limit of 15 minutes: once nobody has been connected for that long, this helper ends itself and what it started; unattendedLimitMinutes is 1, which counts as 15 (the bounds are 15 and 43200)\n"},
		{`{"unattendedLimitMinutes": 1000000000000}`, "INFO  [Server] /etc/claude-desktop-ssh/settings.json sets an unattended limit of 43200 minutes: once nobody has been connected for that long, this helper ends itself and what it started; unattendedLimitMinutes is 1000000000000, which counts as 43200 (the bounds are 15 and 43200)\n"},
		{`{"unattendedLimitMinutes": 60}`, "INFO  [Server] /etc/claude-desktop-ssh/settings.json sets an unattended limit of 60 minutes: once nobody has been connected for that long, this helper ends itself and what it started\n"},
		{`{"unattendedLimitMinutes": 30, "other": 1, "x": {"y": 2}}`, "INFO  [Server] /etc/claude-desktop-ssh/settings.json sets an unattended limit of 30 minutes: once nobody has been connected for that long, this helper ends itself and what it started; 2 key(s) are unknown to this version of the helper and ignored, the first of them \"other\" (a misspelt key sets nothing)\n"},
		{`{"unattendedLimitMinutes": 30, "p": "aaa"}`, "INFO  [Server] /etc/claude-desktop-ssh/settings.json sets an unattended limit of 30 minutes: once nobody has been connected for that long, this helper ends itself and what it started; 1 key(s) are unknown to this version of the helper and ignored, the first of them \"p\" (a misspelt key sets nothing)\n"},
		{`{"unattendedLimitMinutes": 0}`, "WARN  [Server] /etc/claude-desktop-ssh/settings.json can't be used, so no Claude Code is started until it is fixed. unattendedLimitMinutes must be a whole number of minutes, 1 or more, with no quotes. To set no limit, remove it. No unattended limit applies meanwhile.\n"},
		{"\x00mode666", "WARN  [Server] /etc/claude-desktop-ssh/settings.json can't be used, so no Claude Code is started until it is fixed. `/etc/claude-desktop-ssh/settings.json` can be changed by accounts other than its owner (mode 0666). Remove that write access. No unattended limit applies meanwhile.\n"},
		{"\x00owner", "WARN  [Server] /etc/claude-desktop-ssh/settings.json can't be used, so no Claude Code is started until it is fixed. `/etc/claude-desktop-ssh/settings.json` belongs to the account that signs in (uid 1000). Root must own the settings file and every folder above it. No unattended limit applies meanwhile.\n"},
		{"\x00dir777", "WARN  [Server] /etc/claude-desktop-ssh/settings.json can't be used, so no Claude Code is started until it is fixed. `/etc/claude-desktop-ssh` can be changed by accounts other than its owner (mode 0777). Remove that write access. No unattended limit applies meanwhile.\n"},
		{`{`, "WARN  [Server] /etc/claude-desktop-ssh/settings.json can't be used, so no Claude Code is started until it is fixed. It must hold one JSON object, such as {\"unattendedLimitMinutes\": 60}. No unattended limit applies meanwhile.\n"},
	} {
		h := judgeHostLimit(hostLimitTestPath, []byte(tc.body))
		if f, ok := fileFaultsForTest[tc.body]; ok {
			h = hostLimit{path: hostLimitTestPath}.withFault(f.fault, f.detail)
		}
		// Only the lines of the settings file count, as the capture is shared.
		got := linesWith(captureLog(t, h.logStart), " [Server] ", "settings.json")
		if want := strings.TrimSuffix(tc.want, "\n"); len(got) != 1 || got[0] != want {
			t.Errorf("body %s logs\n%q\nwant\n%q", tc.body, got, want)
		}
	}
}

// TestHostLimitSpawnRefusal: a spawn of a Claude Code shape gets the -32010 frame of
// 13047a51 while the file cannot be used, and a plain spawn gets none (measured on Linux).
func TestHostLimitSpawnRefusal(t *testing.T) {
	cc := &spawnParams{ID: "hs-cc-v0", Command: "/x/ccd-cli", Args: []string{"-p", "x", "--no-session-persistence"}}
	plain := &spawnParams{ID: "hs-pl-v0", Command: "/x/plain"}
	for _, tc := range []struct {
		h    hostLimit
		want string
	}{
		{hostLimit{path: hostLimitTestPath}.withFault(hostFaultWritable, "`/etc/claude-desktop-ssh/settings.json` can be changed by accounts other than its owner (mode 0666). Remove that write access."), `{"jsonrpc":"2.0","id":2,"error":{"code":-32010,"message":"Claude Code was not started: this host's settings file for Claude Desktop, /etc/claude-desktop-ssh/settings.json, can't be used. ` + "`/etc/claude-desktop-ssh/settings.json`" + ` can be changed by accounts other than its owner (mode 0666). Remove that write access. Only the host's administrator can change it.","data":{"reason":"host_settings","settingsFault":"writable"}}}`},
		{hostLimit{path: hostLimitTestPath}.withFault(hostFaultOwner, "`/etc/claude-desktop-ssh/settings.json` belongs to the account that signs in (uid 1000). Root must own the settings file and every folder above it."), `{"jsonrpc":"2.0","id":2,"error":{"code":-32010,"message":"Claude Code was not started: this host's settings file for Claude Desktop, /etc/claude-desktop-ssh/settings.json, can't be used. ` + "`/etc/claude-desktop-ssh/settings.json`" + ` belongs to the account that signs in (uid 1000). Root must own the settings file and every folder above it. Only the host's administrator can change it.","data":{"reason":"host_settings","settingsFault":"owner"}}}`},
		{hostLimit{path: hostLimitTestPath}.withFault(hostFaultWritable, "`/etc/claude-desktop-ssh` can be changed by accounts other than its owner (mode 0777). Remove that write access."), `{"jsonrpc":"2.0","id":2,"error":{"code":-32010,"message":"Claude Code was not started: this host's settings file for Claude Desktop, /etc/claude-desktop-ssh/settings.json, can't be used. ` + "`/etc/claude-desktop-ssh`" + ` can be changed by accounts other than its owner (mode 0777). Remove that write access. Only the host's administrator can change it.","data":{"reason":"host_settings","settingsFault":"writable"}}}`},
		{judgeHostLimit(hostLimitTestPath, []byte(`{"unattendedLimitMinutes": 0}`)), `{"jsonrpc":"2.0","id":2,"error":{"code":-32010,"message":"Claude Code was not started: this host's settings file for Claude Desktop, /etc/claude-desktop-ssh/settings.json, can't be used. unattendedLimitMinutes must be a whole number of minutes, 1 or more, with no quotes. To set no limit, remove it. Only the host's administrator can change it.","data":{"reason":"host_settings","settingsFault":"value"}}}`},
		{judgeHostLimit(hostLimitTestPath, []byte(`[1]`)), `{"jsonrpc":"2.0","id":2,"error":{"code":-32010,"message":"Claude Code was not started: this host's settings file for Claude Desktop, /etc/claude-desktop-ssh/settings.json, can't be used. It must hold one JSON object, such as {\"unattendedLimitMinutes\": 60}. Only the host's administrator can change it.","data":{"reason":"host_settings","settingsFault":"syntax"}}}`},
		{hostLimit{path: hostLimitTestPath}.withFault(hostFaultUnreadable, hostDetailTooLarge), `{"jsonrpc":"2.0","id":2,"error":{"code":-32010,"message":"Claude Code was not started: this host's settings file for Claude Desktop, /etc/claude-desktop-ssh/settings.json, can't be used. It can't be read: it is larger than 64 KiB. Only the host's administrator can change it.","data":{"reason":"host_settings","settingsFault":"unreadable"}}}`},
		{hostLimit{path: hostLimitTestPath}.withFault(hostFaultUnreadable, hostDetailNotFile), `{"jsonrpc":"2.0","id":2,"error":{"code":-32010,"message":"Claude Code was not started: this host's settings file for Claude Desktop, /etc/claude-desktop-ssh/settings.json, can't be used. It can't be read: it is not a regular file. Only the host's administrator can change it.","data":{"reason":"host_settings","settingsFault":"unreadable"}}}`},
		{hostLimit{path: hostLimitTestPath}.withFault(hostFaultUnreadable, hostDetailNoRead), `{"jsonrpc":"2.0","id":2,"error":{"code":-32010,"message":"Claude Code was not started: this host's settings file for Claude Desktop, /etc/claude-desktop-ssh/settings.json, can't be used. The account that signs in can't read it. Give it mode 0644. Only the host's administrator can change it.","data":{"reason":"host_settings","settingsFault":"unreadable"}}}`},
	} {
		var bad *rpc.Response
		logs := captureLog(t, func() { bad = tc.h.spawnRefusal(2, cc) })
		if bad == nil {
			t.Fatalf("fault %q: no refusal", tc.h.fault)
		}
		got, err := json.Marshal(bad)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != tc.want {
			t.Errorf("frame\n%s\nwant\n%s", got, tc.want)
		}
		var e struct{ Error struct{ Message string } }
		_ = json.Unmarshal(got, &e)
		if got := linesWith(logs, "Failed to start process hs-cc-v0: "); len(got) != 1 || got[0] != "ERROR [process.Manager] Failed to start process hs-cc-v0: "+e.Error.Message {
			t.Errorf("log %q, want one failed-start line with the message", got)
		}
		if tc.h.spawnRefusal(2, plain) != nil {
			t.Errorf("fault %q refuses a plain spawn", tc.h.fault)
		}
	}
	// The shapes other than a ccd-cli command follow claudeCodeShape. A stream-json arg
	// on another command and a launcher spawn are refused, and a bare git with a
	// stream-json arg is not.
	bad := hostLimit{path: hostLimitTestPath}.withFault(hostFaultValue, hostDetailValue)
	for _, tc := range []struct {
		name string
		p    *spawnParams
		want bool
	}{
		{"stream-json", &spawnParams{ID: "a", Command: "/x/node", Args: []string{"--output-format=stream-json"}}, true},
		{"launcher", &spawnParams{ID: "b", Command: "/x/plain", Launcher: &[]string{"/x/launch"}}, true},
		{"bare git", &spawnParams{ID: "c", Command: "git", Args: []string{"--output-format=stream-json"}}, false},
	} {
		var got *rpc.Response
		captureLog(t, func() { got = bad.spawnRefusal(2, tc.p) })
		if (got != nil) != tc.want {
			t.Errorf("%s: refused %v, want %v", tc.name, got != nil, tc.want)
		}
	}
	if (hostLimit{path: hostLimitTestPath, minutes: 15}).spawnRefusal(2, cc) != nil {
		t.Error("a usable file refuses a Claude Code spawn")
	}
}

// TestCapabilitiesLimitMember: unattendedLimitMinutes comes after peerCheck while a limit
// is set, and is absent with none (13047a51, measured on Linux).
func TestCapabilitiesLimitMember(t *testing.T) {
	for _, tc := range []struct {
		limit int
		tail  string
	}{{15, `"peerCheck":"off","unattendedLimitMinutes":15}`}, {0, `"peerCheck":"off"}`}} {
		b, err := json.Marshal(rpc.CapabilitiesReply{Version: "v", Features: []string{}, PeerCheck: "off", Limit: tc.limit})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(string(b), tc.tail) {
			t.Errorf("limit %d: %s, want the tail %s", tc.limit, b, tc.tail)
		}
	}
}

// TestSocketHostLimit: over the socket, a daemon whose host settings file cannot be used
// refuses a ccd-cli spawn with the -32010 frame, starts a plain spawn, and has no limit
// member. A daemon with a limit shows it after peerCheck (13047a51, measured on Linux).
// The -32010 answer for /nonexistent/ccd-cli rests on the place of the refusal before the
// pty key, cwd, command lookup and id tests, which is claustrum's own, not measured.
func TestSocketHostLimit(t *testing.T) {
	const ccSpawn = `{"jsonrpc":"2.0","id":2,"method":"process.spawn","params":{"id":"hs-cc-v0","command":"/nonexistent/ccd-cli","args":["-p","x","--no-session-persistence"]}}`
	const caps = `{"jsonrpc":"2.0","id":1,"method":"server.capabilities","params":{}}`
	for _, tc := range []struct {
		name  string
		h     hostLimit
		caps  string
		spawn string
	}{
		{"fault", judgeHostLimit(hostLimitTestPath, []byte(`{"unattendedLimitMinutes": 0}`)), `"peerCheck":"off"}}`,
			`{"jsonrpc":"2.0","id":2,"error":{"code":-32010,"message":"Claude Code was not started: this host's settings file for Claude Desktop, /etc/claude-desktop-ssh/settings.json, can't be used. unattendedLimitMinutes must be a whole number of minutes, 1 or more, with no quotes. To set no limit, remove it. Only the host's administrator can change it.","data":{"reason":"host_settings","settingsFault":"value"}}}`},
		{"limit", judgeHostLimit(hostLimitTestPath, []byte(`{"unattendedLimitMinutes": 1}`)), `"peerCheck":"off","unattendedLimitMinutes":15}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, sock := newRunningServer(t)
			s.mu.Lock()
			s.hostLimit = tc.h
			s.mu.Unlock()
			cl := dial(t, sock)
			if got := string(cl.call(authed(caps))); !strings.HasSuffix(got, tc.caps) {
				t.Errorf("capabilities %s, want the tail %s", got, tc.caps)
			}
			if tc.spawn != "" {
				if got := string(cl.call(authed(ccSpawn))); got != tc.spawn {
					t.Errorf("spawn\n%s\nwant\n%s", got, tc.spawn)
				}
			}
			if got := string(cl.call(spawnReq(t, 3, "hs-pl", "stdout3"))); !strings.Contains(got, `"result":{"success":true}`) {
				t.Errorf("plain spawn %s, want success", got)
			}
		})
	}
}

// fileFaultsForTest are the file rule faults of the rows, keyed by a body that no file holds.
var fileFaultsForTest = map[string]hostFileFault{
	"\x00mode666": {hostFaultWritable, "`/etc/claude-desktop-ssh/settings.json` can be changed by accounts other than its owner (mode 0666). Remove that write access."},
	"\x00owner":   {hostFaultOwner, "`/etc/claude-desktop-ssh/settings.json` belongs to the account that signs in (uid 1000). Root must own the settings file and every folder above it."},
	"\x00dir777":  {hostFaultWritable, "`/etc/claude-desktop-ssh` can be changed by accounts other than its owner (mode 0777). Remove that write access."},
}

// TestHostLimitUnknownKeys: the count of the other keys of the top object and the first of
// them. 13047a51 counts 2 for "other" and "x" with a nested key, and 1 for "p" (measured
// on Linux). The file order, and one count for a key twice, are claustrum's own.
func TestHostLimitUnknownKeys(t *testing.T) {
	for _, tc := range []struct {
		body  string
		n     int
		first string
	}{
		{`{"unattendedLimitMinutes": 30, "other": 1, "x": {"y": 2}}`, 2, "other"},
		{`{"zeta": 1, "unattendedLimitMinutes": 30, "alpha": 2}`, 2, "zeta"},
		{`{"unattendedLimitMinutes": 30}`, 0, ""},
		{`{"a": 1, "a": 2}`, 1, "a"},
	} {
		h := judgeHostLimit(hostLimitTestPath, []byte(tc.body))
		if h.unknown != tc.n || h.firstUnknown != tc.first {
			t.Errorf("%s: got %d, %q, want %d, %q", tc.body, h.unknown, h.firstUnknown, tc.n, tc.first)
		}
	}
}

// linesWith gives the lines of logs that hold each of subs.
func linesWith(logs string, subs ...string) []string {
	var out []string
	for _, l := range strings.Split(logs, "\n") {
		ok := true
		for _, sub := range subs {
			ok = ok && strings.Contains(l, sub)
		}
		if ok {
			out = append(out, l)
		}
	}
	return out
}
