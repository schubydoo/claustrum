package main

import (
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The tests of the peerCheck member of server.capabilities and of the
// CLAUDE_SSH_PEER_CHECK variable. Each test sets the variable with t.Setenv, so none
// of them runs in parallel.

// wantPeerCheckLine is the line of a start with CLAUDE_SSH_PEER_CHECK=1, as the
// daemon log holds it. The text of macOS and Windows is the one of 5fd08069 (macOS
// and Windows VMs). The Linux text is the one of 5fd08069 too (round task011-01,
// Linux VM), with the own text of this host.
func wantPeerCheckLine() string {
	if own := daemonNSText(); own.names() {
		return "INFO  [Server] serving only callers in \"" + own.String() + "\"\n"
	}
	return "WARN  [Server] this system names no namespaces: cannot tell callers in a sandbox from others, serving all\n"
}

// wantCapabilityFeatures is the features array in the order of the frame.
// process.kill.farewell comes directly after process.stdin.offset, and
// files.list.includeDotNames after files.extract_tar.execBit. server.peer_check and
// git.shadow_dir come directly after launcher.managed. files.list.junctionsAsDirs
// comes after files.list.includeDotNames, and files.read.real after git.shadow_dir
// (683cc6a9), and git.hooks_null_device between them (be1c1ca5). Windows lists no git.worktree.external_root and no
// process.spawn.link_bound.
func wantCapabilityFeatures() string {
	const head = `"process.stdin.offset","process.kill.farewell","files.extract_tar.execBit","files.list.includeDotNames","files.list.junctionsAsDirs","git.status.baseRepo","git.info.discovered_root","git.worktree_create.timeoutMs","git.worktree_create.existingBranch","git.worktree_remove.unpushedGuard","process.spawn.shellAgentSocket","process.spawn.failure_reason","server.line_end","launcher.managed","server.peer_check","git.shadow_dir","git.hooks_null_device","files.read.real",`
	if runtime.GOOS == "windows" {
		return head + `"server.instance_id"`
	}
	return head + `"git.worktree.external_root","process.spawn.link_bound","server.instance_id"`
}

// wantCapabilitiesFrame is the whole frame of a test server, byte for byte. by is
// the peerCheckBy array text, or empty for no member.
func wantCapabilitiesFrame(peerCheck, by string) string {
	tail := `"peerCheck":"` + peerCheck + `"`
	if by != "" {
		tail += `,"peerCheckBy":` + by
	}
	return `{"jsonrpc":"2.0","id":1,"result":{"version":"` + Version + `","methods":["server.ping","server.capabilities","server.shutdown","files.list","files.validate","files.stat","files.read","files.extract_tar","git.info","git.status","git.list_branches","git.worktree_create","git.worktree_remove","launcher.resolve","process.spawn","process.stdin","process.kill","process.killAndWait","process.reattach","plugins.prune","files.realpath"],"instanceId":"` + testInstanceID + `","startedAt":1700000000000,"features":[` + wantCapabilityFeatures() + `],` + tail + `}}`
}

// stubDaemonNSText gives the daemons of one test the own text t.
func stubDaemonNSText(t *testing.T, own nsText) {
	t.Helper()
	old := readDaemonNSText
	t.Cleanup(func() { readDaemonNSText = old })
	readDaemonNSText = func() nsText { return own }
}

// measuredOwn is the own text of 5fd08069 in round task011-01 (Linux VM).
var measuredOwn = nsText{user: "user:[4026531837]", net: "net:1", pidfd: true}

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
		{name: "1", value: "1", want: "on"},
	}
	stubDaemonNSText(t, measuredOwn)
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
			s.peerCheck = startPeerCheck()
			by := ""
			if tc.want == "on" {
				by = `["network","pidfd"]`
			}
			for _, params := range []string{``, `,"params":{}`} {
				got := dispatchRaw(t, s, `{"jsonrpc":"2.0","id":1,"method":"server.capabilities"`+params+`,"auth":"`+testToken+`"}`)
				if want := wantCapabilitiesFrame(tc.want, by); got != want {
					t.Errorf("frame with %q =\n%s\nwant\n%s", params, got, want)
				}
			}
			wantLines := 0
			if tc.want == "on" {
				wantLines = 1
			}
			const line = "INFO  [Server] serving only callers in \"user:[4026531837] net:1 pidfd\"\n"
			if n := strings.Count(logs.String(), line); n != wantLines {
				t.Errorf("the peer check line appears %d times, want %d. Log:\n%s", n, wantLines, logs.String())
			}
			if l := logs.String(); wantLines == 0 && (strings.Contains(l, "[Server] serving only callers") || strings.Contains(l, "serving all")) {
				t.Errorf("a start with the value %q logs a peer check line:\n%s", tc.value, logs.String())
			}
		})
	}
}

// TestPeerCheckStartServesEveryCaller: a daemon that starts with the value 1 writes
// the line once and serves a caller in its own process. Over the socket it answers
// the state of the own text of this host: "unavailable" with no peerCheckBy on
// macOS and Windows (R0), and the parts of the kernel on Linux.
func TestPeerCheckStartServesEveryCaller(t *testing.T) {
	t.Setenv(peerCheckEnv, "1")
	logs := captureLogBuf(t)
	s, sock := bootLineServer(t, false)
	t.Cleanup(func() { s.signalShutdown(); s.closeAll(sock) })
	if n := strings.Count(logs.String(), wantPeerCheckLine()); n != 1 {
		t.Errorf("the peer check line appears %d times in the start, want 1. Log:\n%s", n, logs.String())
	}
	own := daemonNSText()
	if runtime.GOOS != "linux" && own.names() {
		t.Errorf("own text = %q on %s, want none", own.String(), runtime.GOOS)
	}
	// The list ends with server.instance_id, or with the job name on a Windows daemon
	// with jobs on.
	features := s.featuresFor()
	tail := `"` + features[len(features)-1] + `"],"peerCheck":"` + own.state() + `"`
	if by := own.by(); by != nil {
		tail += `,"peerCheckBy":["` + strings.Join(by, `","`) + `"]`
	}
	cl := dial(t, sock)
	caps := string(cl.call(authed(`{"jsonrpc":"2.0","id":1,"method":"server.capabilities"}`)))
	if !strings.HasSuffix(caps, tail+"}}") {
		t.Errorf("server.capabilities = %s, want it to end with %s}}", caps, tail)
	}
	if pong := string(cl.call(authed(`{"jsonrpc":"2.0","id":2,"method":"server.ping"}`))); pong != `{"jsonrpc":"2.0","id":2,"result":{"pong":true}}` {
		t.Errorf("server.ping = %s, want the pong frame", pong)
	}
	if n := s.peerCheck.refused.Load(); n != 0 {
		t.Errorf("the daemon refused %d callers, want 0", n)
	}
}

// TestPeerCheckFailedRemovalLogsOneLine: a removal that fails does not stop the
// start. The daemon logs one WARN line that names the variable, and the answer
// stays the one of the value that it read.
func TestPeerCheckFailedRemovalLogsOneLine(t *testing.T) {
	t.Setenv(peerCheckEnv, "0")
	old := unsetenv
	t.Cleanup(func() { unsetenv = old })
	unsetenv = func(string) error { return errors.New("no removal") }
	logs := captureLogBuf(t)
	if startPeerCheck() != nil {
		t.Error("startPeerCheck() gives a gate for the value 0")
	}
	want := "WARN  [Server] cannot remove CLAUDE_SSH_PEER_CHECK from the daemon environment (no removal): child processes inherit it"
	if n := strings.Count(logs.String(), want); n != 1 {
		t.Errorf("log holds the line %d times, want 1:\n%s", n, logs.String())
	}
}

// TestPeerCheckOwnTextAndStates: the own text joins its parts with one space, as
// 5fd08069 logs it (round task011-01, Linux VM). All parts give "on" with
// ["network","pidfd"] (cell P1). The other rows are claustrum's own names.
func TestPeerCheckOwnTextAndStates(t *testing.T) {
	cases := []struct {
		own         nsText
		text, state string
		by          []string
	}{
		{measuredOwn, "user:[4026531837] net:1 pidfd", "on", []string{"network", "pidfd"}},
		{nsText{net: "net:1", pidfd: true}, "net:1 pidfd", "network_only", []string{"network", "pidfd"}},
		{nsText{user: "user:[4026531837]", net: "net:1"}, "user:[4026531837] net:1", "by_pid", []string{"network"}},
		{nsText{user: "user:[4026531837]", pidfd: true}, "user:[4026531837] pidfd", "on", []string{"pidfd"}},
		{nsText{pidfd: true}, "pidfd", "unavailable", nil},
		{nsText{}, "", "unavailable", nil},
	}
	for _, tc := range cases {
		if got := tc.own.String(); got != tc.text {
			t.Errorf("text of %+v = %q, want %q", tc.own, got, tc.text)
		}
		if got := tc.own.state(); got != tc.state {
			t.Errorf("state of %q = %q, want %q", tc.text, got, tc.state)
		}
		if got := tc.own.by(); strings.Join(got, ",") != strings.Join(tc.by, ",") || (got == nil) != (tc.by == nil) {
			t.Errorf("by of %q = %#v, want %#v", tc.text, got, tc.by)
		}
	}
}

// TestPeerCheckStateFrames: the members of each state, byte for byte. An
// "unavailable" daemon has no peerCheckBy member.
func TestPeerCheckStateFrames(t *testing.T) {
	cases := []struct {
		own       nsText
		state, by string
	}{
		{nsText{net: "net:7"}, "network_only", `["network"]`},
		{nsText{user: "user:[1]", net: "net:7"}, "by_pid", `["network"]`},
		{nsText{}, "unavailable", ""},
	}
	for _, tc := range cases {
		t.Setenv(peerCheckEnv, "1")
		stubDaemonNSText(t, tc.own)
		captureLogBuf(t)
		s := newTestServer(t)
		s.peerCheck = startPeerCheck()
		got := dispatchRaw(t, s, `{"jsonrpc":"2.0","id":1,"method":"server.capabilities","auth":"`+testToken+`"}`)
		if want := wantCapabilitiesFrame(tc.state, tc.by); got != want {
			t.Errorf("frame of %q =\n%s\nwant\n%s", tc.own.String(), got, want)
		}
	}
}

// TestPeerRefusalLineBytes: the refusal frame of each reason, byte for byte, with
// "id":null and the newline (round task011-01, cells P2, P3 and P7, Linux VM).
func TestPeerRefusalLineBytes(t *testing.T) {
	for _, reason := range []string{"other_namespace", "unreadable", "lookup_failed"} {
		want := `{"jsonrpc":"2.0","id":null,"error":{"code":-32006,"message":"Refused: this server could not confirm that the caller is in its own namespaces","data":{"reason":"` + reason + `"}}}` + "\n"
		want = string(stampEnd([]byte(want), 0)) // the end member is pinned by TestPeerRefusalLineCarriesEndMember
		if got := string(peerRefusalLine(reason)); got != want {
			t.Errorf("refusal line =\n%q\nwant\n%q", got, want)
		}
	}
}

// TestPeerRefusalLoggedNumbers: refusals 1 to 10 get a log line, and 11 and 12 do
// not (cell P6).
func TestPeerRefusalLoggedNumbers(t *testing.T) {
	for n := int64(1); n <= 12; n++ {
		want := n <= 10
		if got := peerRefusalLogged(n); got != want {
			t.Errorf("peerRefusalLogged(%d) = %v, want %v", n, got, want)
		}
	}
}

// TestJudgePeerReasons: the reason and the detail of each lookup outcome.
func TestJudgePeerReasons(t *testing.T) {
	denied := &fs.PathError{Op: "readlink", Path: "/proc/2308/ns/user", Err: syscall.EACCES}
	// The text of ENOENT differs by OS, so the detail comes from the error itself.
	gone := &fs.PathError{Op: "readlink", Path: "/proc/9/ns/user", Err: syscall.ENOENT}
	cases := []struct {
		name           string
		peer           nsText
		err            error
		reason, detail string
	}{
		{"same", measuredOwn, nil, "", ""},
		{"other user", nsText{user: "user:[4026532385]", net: "net:1", pidfd: true}, nil, "other_namespace", `"user:[4026532385] net:1 pidfd"`},
		{"permission", nsText{}, denied, "unreadable", "readlink /proc/2308/ns/user: permission denied"},
		{"exited", nsText{}, errPeerExited, "lookup_failed", "the caller has exited"},
		{"gone", nsText{}, gone, "lookup_failed", gone.Error()},
	}
	for _, tc := range cases {
		reason, detail := judgePeer(measuredOwn, tc.peer, tc.err)
		if reason != tc.reason || detail != tc.detail {
			t.Errorf("%s: judgePeer = (%q, %q), want (%q, %q)", tc.name, reason, detail, tc.reason, tc.detail)
		}
	}
}

// refusalLines is the text of the refusal lines in the log capture. The capture
// is shared with the late lines of daemons of earlier tests, so a test reads only
// these lines.
func refusalLines(logs *syncBuffer) string {
	var b strings.Builder
	for _, l := range strings.SplitAfter(logs.String(), "\n") {
		if strings.HasPrefix(l, "WARN  [Server] refused caller number ") {
			b.WriteString(l)
		}
	}
	return b.String()
}

// peerAdmitOnce runs admit on one end of a pipe and returns its answer and the
// bytes that the other end read until EOF.
func peerAdmitOnce(t *testing.T, g *peerGate) (bool, string) {
	t.Helper()
	srv, cli := net.Pipe()
	t.Cleanup(func() { _ = cli.Close(); _ = srv.Close() })
	got := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(cli)
		got <- string(b)
	}()
	ok := g.admit(srv)
	if ok {
		_ = srv.Close()
	}
	select {
	case b := <-got:
		return ok, b
	case <-time.After(10 * time.Second):
		t.Fatal("the caller end read no EOF within 10 s")
		return ok, ""
	}
}

// TestPeerGateRefusalLinesAndCount: each refused caller gets the frame and an EOF.
// The log has lines for the refusals 1 to 10 only (cell P6), and the line of
// round task011-01 (cell P2, Linux VM). A caller that exited gets the frame with
// lookup_failed and no line, and it counts (cell P7, the count is claustrum's
// own). A caller with the own text is served and reads nothing.
func TestPeerGateRefusalLinesAndCount(t *testing.T) {
	other := nsText{user: "user:[4026532385]", net: "net:1", pidfd: true}
	var exited bool
	old := lookupPeerOf
	t.Cleanup(func() { lookupPeerOf = old })
	lookupPeerOf = func(net.Conn, nsText) (nsText, bool, error) {
		if exited {
			return nsText{}, true, errPeerExited
		}
		return other, false, nil
	}
	logs := captureLogBuf(t)
	g := &peerGate{own: measuredOwn}
	frame := string(peerRefusalLine("other_namespace"))
	for i := 1; i <= 12; i++ {
		if ok, b := peerAdmitOnce(t, g); ok || b != frame {
			t.Fatalf("caller %d: admit = %v, read %q, want false and %q", i, ok, b, frame)
		}
	}
	line := func(n int) string {
		return "WARN  [Server] refused caller number " + strconv.Itoa(n) + ` (other_namespace): "user:[4026532385] net:1 pidfd"; this server's namespaces are "user:[4026531837] net:1 pidfd"` + "\n"
	}
	var want strings.Builder
	for n := 1; n <= 10; n++ {
		want.WriteString(line(n))
	}
	if got := refusalLines(logs); got != want.String() {
		t.Errorf("refusal lines =\n%s\nwant\n%s", got, want.String())
	}
	// The number 5 gets a line for a caller that did not exit.
	g.refused.Store(4)
	exited = true
	if ok, b := peerAdmitOnce(t, g); ok || b != string(peerRefusalLine("lookup_failed")) {
		t.Errorf("exited caller: admit = %v, read %q", ok, b)
	}
	if got := refusalLines(logs); got != want.String() {
		t.Errorf("an exited caller wrote a line:\n%s", got)
	}
	if n := g.refused.Load(); n != 5 {
		t.Errorf("refused = %d, want 5", n)
	}
	lookupPeerOf = func(net.Conn, nsText) (nsText, bool, error) { return measuredOwn, false, nil }
	if ok, b := peerAdmitOnce(t, g); !ok || b != "" {
		t.Errorf("a caller with the own text: admit = %v, read %q, want true and nothing", ok, b)
	}
	if n := g.refused.Load(); n != 5 {
		t.Errorf("refused = %d after a served caller, want 5", n)
	}
}

// TestPeerGateWithNoOwnTextServesAll: a gate that names no namespace, and no gate,
// serve every caller without a lookup.
func TestPeerGateWithNoOwnTextServesAll(t *testing.T) {
	old := lookupPeerOf
	t.Cleanup(func() { lookupPeerOf = old })
	lookupPeerOf = func(net.Conn, nsText) (nsText, bool, error) {
		t.Error("a lookup ran")
		return nsText{}, false, errPeerExited
	}
	var none *peerGate
	for _, g := range []*peerGate{none, {own: nsText{pidfd: true}}} {
		if ok, b := peerAdmitOnce(t, g); !ok || b != "" {
			t.Errorf("gate %+v: admit = %v, read %q", g, ok, b)
		}
	}
}

// The refusal is the first line of its connection, so its end member has at 0
// (build 52d82f0a, measured on Linux).
func TestPeerRefusalLineCarriesEndMember(t *testing.T) {
	want := `{"jsonrpc":"2.0","id":null,"error":{"code":-32006,"message":"Refused: this server could not confirm that the caller is in its own namespaces","data":{"reason":"unreadable"}},"end":{"at":0,"len":173}}` + "\n"
	if got := string(peerRefusalLine("unreadable")); got != want {
		t.Errorf("peerRefusalLine = %q, want %q", got, want)
	}
}
