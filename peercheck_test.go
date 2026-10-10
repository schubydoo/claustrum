package main

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// The tests of the peerCheck member of server.capabilities and of the
// CLAUDE_SSH_PEER_CHECK variable. Each test sets the variable with t.Setenv, so none
// of them runs in parallel.

// wantPeerCheckLine is the line of a start with CLAUDE_SSH_PEER_CHECK=1, as the
// daemon log holds it. The text of macOS and Windows is the one of 5fd08069 (macOS
// and Windows VMs). The Linux text is claustrum's own.
func wantPeerCheckLine() string {
	if runtime.GOOS == "linux" {
		return "WARN  [Server] CLAUDE_SSH_PEER_CHECK is set: this build has no peer check on Linux, serving all callers\n"
	}
	return "WARN  [Server] this system names no namespaces: cannot tell callers in a sandbox from others, serving all\n"
}

// wantCapabilityFeatures is the features array in the order of the frame.
// server.peer_check comes directly after launcher.managed. Windows lists no
// git.worktree.external_root.
func wantCapabilityFeatures() string {
	const head = `"process.stdin.offset","git.status.baseRepo","git.info.discovered_root","git.worktree_create.timeoutMs","git.worktree_create.existingBranch","git.worktree_remove.unpushedGuard","process.spawn.shellAgentSocket","launcher.managed","server.peer_check",`
	if runtime.GOOS == "windows" {
		return head + `"server.instance_id"`
	}
	return head + `"git.worktree.external_root","server.instance_id"`
}

// wantCapabilitiesFrame is the whole frame of a test server, byte for byte.
func wantCapabilitiesFrame(peerCheck string) string {
	return `{"jsonrpc":"2.0","id":1,"result":{"version":"` + Version + `","methods":["server.ping","server.capabilities","server.shutdown","files.list","files.validate","files.stat","files.read","files.extract_tar","git.info","git.status","git.list_branches","git.worktree_create","git.worktree_remove","launcher.resolve","process.spawn","process.stdin","process.kill","process.killAndWait","process.reattach","plugins.prune"],"instanceId":"` + testInstanceID + `","startedAt":1700000000000,"features":[` + wantCapabilityFeatures() + `],"peerCheck":"` + peerCheck + `"}}`
}

// TestPeerCheckValueTable: only the exact value 1 of CLAUDE_SSH_PEER_CHECK changes
// the peerCheck member, and only that value writes the line. The frame is compared
// byte for byte, so the test also holds the member order and the features order.
func TestPeerCheckValueTable(t *testing.T) {
	cases := []struct {
		name, value string
		unset       bool
		want        string
	}{
		{name: "no variable", unset: true, want: "off"},
		{name: "empty", value: "", want: "off"},
		{name: "0", value: "0", want: "off"},
		{name: "off", value: "off", want: "off"},
		{name: "true", value: "true", want: "off"},
		{name: "yes", value: "yes", want: "off"},
		{name: "on", value: "on", want: "off"},
		{name: "2", value: "2", want: "off"},
		{name: "01", value: "01", want: "off"},
		{name: "space then 1", value: " 1", want: "off"},
		{name: "1", value: "1", want: "unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(peerCheckEnv, tc.value)
			if tc.unset {
				if err := os.Unsetenv(peerCheckEnv); err != nil {
					t.Fatal(err)
				}
			}
			logs := captureLogBuf(t)
			s := newTestServer(t)
			s.peerCheckAsked = startPeerCheck()
			for _, params := range []string{``, `,"params":{}`} {
				got := dispatchRaw(t, s, `{"jsonrpc":"2.0","id":1,"method":"server.capabilities"`+params+`,"auth":"`+testToken+`"}`)
				if want := wantCapabilitiesFrame(tc.want); got != want {
					t.Errorf("frame with %q =\n%s\nwant\n%s", params, got, want)
				}
			}
			wantLines := 0
			if tc.want == "unavailable" {
				wantLines = 1
			}
			if n := strings.Count(logs.String(), wantPeerCheckLine()); n != wantLines {
				t.Errorf("the peer check line appears %d times, want %d. Log:\n%s", n, wantLines, logs.String())
			}
			if wantLines == 0 && strings.Contains(logs.String(), "serving all") {
				t.Errorf("a start with the value %q logs a peer check line:\n%s", tc.value, logs.String())
			}
		})
	}
}

// TestPeerCheckStartServesEveryCaller: a daemon that starts with the value 1 writes
// the line once, answers "unavailable" over the socket and serves the caller.
func TestPeerCheckStartServesEveryCaller(t *testing.T) {
	t.Setenv(peerCheckEnv, "1")
	logs := captureLogBuf(t)
	s, sock := bootLineServer(t, false)
	t.Cleanup(func() { s.signalShutdown(); s.closeAll(sock) })
	if n := strings.Count(logs.String(), wantPeerCheckLine()); n != 1 {
		t.Errorf("the peer check line appears %d times in the start, want 1. Log:\n%s", n, logs.String())
	}
	cl := dial(t, sock)
	caps := string(cl.call(authed(`{"jsonrpc":"2.0","id":1,"method":"server.capabilities"}`)))
	if !strings.HasSuffix(caps, `"server.instance_id"],"peerCheck":"unavailable"}}`) {
		t.Errorf("server.capabilities = %s, want it to end with the features and \"peerCheck\":\"unavailable\"", caps)
	}
	if strings.Contains(caps, "peerCheckBy") {
		t.Errorf("server.capabilities = %s, want no peerCheckBy member", caps)
	}
	if pong := string(cl.call(authed(`{"jsonrpc":"2.0","id":2,"method":"server.ping"}`))); pong != `{"jsonrpc":"2.0","id":2,"result":{"pong":true}}` {
		t.Errorf("server.ping = %s, want the pong frame", pong)
	}
}
