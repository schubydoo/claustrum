package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// replyLine is the whole reply line of request id with the given result, without
// the end member.
func replyLine(id int, result string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, id, result)
}

// exitFrameOf returns the exit frame of id from frames, as the wire bytes.
func exitFrameOf(t *testing.T, frames []streamFrame) string {
	t.Helper()
	for _, f := range frames {
		if f.Stream == "exit" {
			b, err := json.Marshal(f)
			if err != nil {
				t.Fatalf("marshal exit frame: %v", err)
			}
			return string(b)
		}
	}
	t.Fatal("no exit frame")
	return ""
}

// farewellPending reports whether the record id holds a pending farewell.
func farewellPending(s *server, id string) bool {
	p := s.procs.get(id)
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.farewell != nil
}

// waitFarewellPending polls, with a limit, until the record id holds a farewell.
func waitFarewellPending(t *testing.T, s *server, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !farewellPending(s, id) {
		if time.Now().After(deadline) {
			t.Fatalf("no pending farewell on %s", id)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// The rule checks of a farewell and their order (measured on Linux, build
// 52d82f0a). The unit test covers the 30 s cut of graceMs and both sides of the cap.
func TestReadFarewellRules(t *testing.T) {
	big := base64.StdEncoding.EncodeToString(make([]byte, farewellMaxBytes))
	over := base64.StdEncoding.EncodeToString(make([]byte, farewellMaxBytes+1))
	for _, tc := range []struct {
		farewell, grace string // raw JSON, "" for absent
		rule            string
		ask             bool
		graceMs         int
	}{
		{`"aGkK"`, `2000`, "", true, 2000},
		{`"aGkK"`, `45000`, "", true, 30000},
		{`"` + big + `"`, `100`, "", true, 100},
		{`"` + over + `"`, `100`, "over the cap", false, 0},
		{`"!!!"`, `100`, "not base64", false, 0},
		{`"aGkK"`, ``, "a farewell with no graceMs", false, 0},
		{``, `100`, "graceMs with no farewell", false, 0},
		{`5`, `100`, "of the wrong type", false, 0},
		{`"!!!"`, ``, "a farewell with no graceMs", false, 0},
		{`5`, `"x"`, "of the wrong type", false, 0},
		{`"!!!"`, `"x"`, "of the wrong type", false, 0},
		{`"aGkK"`, `-5`, "a farewell with no graceMs", false, 0},
		{`""`, `0`, "", false, 0},
		{`null`, `100`, "graceMs with no farewell", false, 0},
		{`"aGkK"`, `null`, "a farewell with no graceMs", false, 0},
		{`"aGkK"`, `1.5`, "of the wrong type", false, 0},
		{``, ``, "", false, 0},
	} {
		ask, rule := readFarewell(json.RawMessage(tc.farewell), json.RawMessage(tc.grace))
		if rule != tc.rule || (ask != nil) != tc.ask {
			t.Errorf("readFarewell(%.20s, %s) = %v, %q, want ask %v, %q", tc.farewell, tc.grace, ask != nil, rule, tc.ask, tc.rule)
			continue
		}
		if ask != nil && ask.grace != time.Duration(tc.graceMs)*time.Millisecond {
			t.Errorf("readFarewell(%.20s, %s) grace = %v, want %d ms", tc.farewell, tc.grace, ask.grace, tc.graceMs)
		}
	}
}

// process.kill with a farewell answers queued at once, the child reads the
// farewell, and its exit with code 0 carries killedBy client and no signal. The
// exit line says "asked to end by this daemon (client)" (measured on Linux and
// macOS, build 52d82f0a).
func TestFarewellKillQueuedChildReadsIt(t *testing.T) {
	logs := captureLogBuf(t)
	sock := startSocketServer(t)
	cl := dial(t, sock)
	cl.call(spawnReqArgs(t, 1, "fw1", "read-line"))
	start := time.Now()
	got := string(cl.call(authed(`{"jsonrpc":"2.0","id":2,"method":"process.kill","params":{"id":"fw1","signal":"SIGTERM","farewell":"aGkK","graceMs":15000}}`)))
	if want := replyLine(2, `{"success":true,"farewellEnd":"queued"}`); got != want {
		t.Fatalf("reply = %s, want %s", got, want)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("queued reply took %v, want at once", d)
	}
	frames := cl.waitExit("fw1")
	if out := streamBytes(t, frames, "stdout"); out != "got:hi\n" {
		t.Errorf("stdout = %q, want %q", out, "got:hi\n")
	}
	if got, want := exitFrameOf(t, frames), `{"type":"stream","processId":"fw1","stream":"exit","seq":2,"exitCode":0,"killedBy":"client"}`; got != want {
		t.Errorf("exit frame = %s, want %s", got, want)
	}
	want := "[process.Manager] Process fw1 exited with code 0, asked to end by this daemon (client)\n"
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("log has no line %q", want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// An unknown id with a farewell gives unsent, and with none the plain reply
// (measured on Linux and macOS, build 52d82f0a).
func TestFarewellKillUnknownID(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	if got, want := string(cl.call(authed(`{"jsonrpc":"2.0","id":1,"method":"process.kill","params":{"id":"nope","farewell":"aGkK","graceMs":100}}`))), replyLine(1, `{"success":true,"farewellEnd":"unsent"}`); got != want {
		t.Errorf("reply = %s, want %s", got, want)
	}
	if got, want := string(cl.call(authed(`{"jsonrpc":"2.0","id":2,"method":"process.kill","params":{"id":"nope"}}`))), replyLine(2, `{"success":true}`); got != want {
		t.Errorf("reply = %s, want %s", got, want)
	}
	if got, want := string(cl.call(authed(`{"jsonrpc":"2.0","id":3,"method":"process.killAndWait","params":{"id":"nope","farewell":"aGkK","graceMs":100}}`))), replyLine(3, `{"found":false,"died":false,"farewellEnd":"unsent"}`); got != want {
		t.Errorf("reply = %s, want %s", got, want)
	}
}

// A farewell that fails a rule gets invalid, one log line with the rule, and the
// signal at once. An empty farewell with graceMs 0 counts as none (measured on
// Linux, build 52d82f0a, and on macOS for the first five rules).
func TestFarewellKillInvalidRules(t *testing.T) {
	logs := captureLogBuf(t)
	sock := startSocketServer(t)
	cl := dial(t, sock)
	over := base64.StdEncoding.EncodeToString(make([]byte, farewellMaxBytes+1))
	for i, tc := range []struct{ members, rule string }{
		{`"farewell":"!!!","graceMs":100`, "not base64"},
		{`"farewell":"aGkK"`, "a farewell with no graceMs"},
		{`"graceMs":100`, "graceMs with no farewell"},
		{`"farewell":5,"graceMs":100`, "of the wrong type"},
		{`"farewell":"` + over + `","graceMs":100`, "over the cap"},
		{`"farewell":"aGkK","graceMs":1.5`, "of the wrong type"},
		{`"farewell":null,"graceMs":100`, "graceMs with no farewell"},
		{`"farewell":"aGkK","graceMs":-5`, "a farewell with no graceMs"},
		{`"farewell":"","graceMs":0`, ""},
	} {
		id := fmt.Sprintf("fv-%d", i)
		cl.call(spawnReqArgs(t, 100+i, id, "sleep", "30"))
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"process.kill","params":{"id":%q,"signal":"SIGTERM",%s}}`, 200+i, id, tc.members)
		got := string(cl.call(authed(body)))
		want := replyLine(200+i, `{"success":true,"farewellEnd":"invalid"}`)
		if tc.rule == "" {
			want = replyLine(200+i, `{"success":true}`)
		}
		if got != want {
			t.Errorf("%s: reply = %s, want %s", tc.members, got, want)
		}
		if f := cl.waitExit(id); exitFrameOfKilledBy(f) != "client" {
			t.Errorf("%s: exit frame killedBy = %q, want client", tc.members, exitFrameOfKilledBy(f))
		}
		line := "[process] kill " + id + ": its farewell is not said: "
		if tc.rule == "" {
			if strings.Contains(logs.String(), line) {
				t.Errorf("%s: log has %q", tc.members, line)
			}
		} else if !strings.Contains(logs.String(), line+tc.rule+"\n") {
			t.Errorf("%s: log has no line %q", tc.members, line+tc.rule)
		}
	}
}

func exitFrameOfKilledBy(frames []streamFrame) string {
	for _, f := range frames {
		if f.Stream == "exit" {
			return f.KilledBy
		}
	}
	return ""
}

// process.killAndWait with a farewell on a child that reads it and exits gives
// left, with no escalated member and exit code 0 (measured on Linux and macOS,
// build 52d82f0a).
func TestFarewellKillAndWaitLeft(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	cl.call(spawnReqArgs(t, 1, "fw5", "read-line"))
	got := string(cl.call(authed(`{"jsonrpc":"2.0","id":2,"method":"process.killAndWait","params":{"id":"fw5","farewell":"aGkK","graceMs":15000}}`)))
	if want := replyLine(2, `{"found":true,"died":true,"farewellEnd":"left"}`); got != want {
		t.Fatalf("reply = %s, want %s", got, want)
	}
	frames := cl.waitExit("fw5")
	if out := streamBytes(t, frames, "stdout"); out != "got:hi\n" {
		t.Errorf("stdout = %q, want %q", out, "got:hi\n")
	}
	if got, want := exitFrameOf(t, frames), `{"type":"stream","processId":"fw5","stream":"exit","seq":2,"exitCode":0,"killedBy":"client"}`; got != want {
		t.Errorf("exit frame = %s, want %s", got, want)
	}
}

// A plain kill from a second connection cuts the pending farewell of a
// killAndWait. Its reply is cut, long before the grace of 10 s (measured on Linux
// and macOS, build 52d82f0a).
func TestFarewellKillAndWaitCut(t *testing.T) {
	s, sock := newRunningServer(t)
	a := dial(t, sock)
	b := dial(t, sock)
	a.call(spawnReqArgs(t, 1, "fw7", "sleep", "30"))
	start := time.Now()
	a.send(authed(`{"jsonrpc":"2.0","id":2,"method":"process.killAndWait","params":{"id":"fw7","farewell":"aGkK","graceMs":10000,"timeoutMs":20000}}`))
	waitFarewellPending(t, s, "fw7")
	if got, want := string(b.call(authed(`{"jsonrpc":"2.0","id":1,"method":"process.kill","params":{"id":"fw7","signal":"SIGTERM"}}`))), replyLine(1, `{"success":true}`); got != want {
		t.Errorf("B reply = %s, want %s", got, want)
	}
	rs := a.waitResponses(2)
	if got, want := string(rs[1]), replyLine(2, `{"found":true,"died":true,"farewellEnd":"cut"}`); got != want {
		t.Errorf("A reply = %s, want %s", got, want)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("cut reply took %v, want well before the grace of 10 s", d)
	}
	if kb := exitFrameOfKilledBy(a.waitExit("fw7")); kb != "client" {
		t.Errorf("exit killedBy = %q, want client", kb)
	}
}

// A plain kill cuts the queued farewell of an earlier process.kill and signals at
// once (measured on Linux, build 52d82f0a).
func TestFarewellPlainKillCutsQueued(t *testing.T) {
	s, sock := newRunningServer(t)
	a := dial(t, sock)
	b := dial(t, sock)
	a.call(spawnReqArgs(t, 1, "k1", "sleep", "30"))
	start := time.Now()
	if got, want := string(a.call(authed(`{"jsonrpc":"2.0","id":2,"method":"process.kill","params":{"id":"k1","signal":"SIGTERM","farewell":"aGkK","graceMs":10000}}`))), replyLine(2, `{"success":true,"farewellEnd":"queued"}`); got != want {
		t.Fatalf("reply = %s, want %s", got, want)
	}
	if !farewellPending(s, "k1") {
		t.Fatal("no pending farewell after queued")
	}
	b.call(authed(`{"jsonrpc":"2.0","id":1,"method":"process.kill","params":{"id":"k1","signal":"SIGTERM"}}`))
	if farewellPending(s, "k1") {
		t.Error("the plain kill left the farewell pending")
	}
	frames := a.waitExit("k1")
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("exit after %v, want well before the grace of 10 s", d)
	}
	if kb := exitFrameOfKilledBy(frames); kb != "client" {
		t.Errorf("exit killedBy = %q, want client", kb)
	}
}

// graceMs over 30000 is cut to 30 s (measured on Linux, build 52d82f0a). The seam
// shrinks the cap, so a graceMs of 45000 signals at the cap.
func TestFarewellGraceIsCut(t *testing.T) {
	old := farewellGraceCapMs
	farewellGraceCapMs = 200
	t.Cleanup(func() { farewellGraceCapMs = old })
	sock := startSocketServer(t)
	cl := dial(t, sock)
	cl.call(spawnReqArgs(t, 1, "gc", "sleep", "30"))
	start := time.Now()
	cl.call(authed(`{"jsonrpc":"2.0","id":2,"method":"process.kill","params":{"id":"gc","signal":"SIGTERM","farewell":"aGkK","graceMs":45000}}`))
	frames := cl.waitExit("gc")
	if d := time.Since(start); d < 150*time.Millisecond || d > 4*time.Second {
		t.Errorf("exit after %v, want about the cap of 200 ms", d)
	}
	if kb := exitFrameOfKilledBy(frames); kb != "client" {
		t.Errorf("exit killedBy = %q, want client", kb)
	}
}

// A server.shutdown during a pending farewell of a killAndWait gives that request
// no reply and no exit frame. The connection ends (measured on Linux and macOS,
// build 52d82f0a). The test then runs the shutdown kill of closeAll itself.
func TestFarewellShutdownGivesNoReply(t *testing.T) {
	s, sock := newRunningServer(t)
	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { nc.Close() })
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(nc)
		for sc.Scan() {
			plain, _, _, _ := splitEnd(append([]byte(nil), sc.Bytes()...))
			lines <- string(plain)
		}
		close(lines)
	}()
	write := func(line string) {
		if _, err := nc.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	write(spawnReqArgs(t, 1, "fs", "sleep", "30"))
	select {
	case got := <-lines:
		if got != replyLine(1, `{"success":true}`) {
			t.Fatalf("spawn reply = %s", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no spawn reply")
	}
	write(authed(`{"jsonrpc":"2.0","id":2,"method":"process.killAndWait","params":{"id":"fs","farewell":"aGkK","graceMs":10000,"timeoutMs":20000}}`))
	waitFarewellPending(t, s, "fs")
	b := dial(t, sock)
	b.send(`{"jsonrpc":"2.0","id":1,"method":"server.shutdown","params":{}}`)
	timeout := time.After(5 * time.Second)
	for {
		select {
		case l, ok := <-lines:
			if ok {
				t.Errorf("line before the end of the connection: %s", l)
				continue
			}
		case <-timeout:
			t.Fatal("the connection did not end")
		}
		break
	}
	if n := s.procs.killAllCount(); n != 1 {
		t.Errorf("shutdown kill reached %d children, want 1", n)
	}
	select {
	case <-s.procs.get("fs").done:
	case <-time.After(10 * time.Second):
		t.Fatal("the shutdown kill did not end the child")
	}
}

// Under -keep-children the shutdown sends the signal of a pending farewell at
// once, and a child with no farewell stays (claustrum's own, not measured: CT-2).
func TestFarewellKeepChildrenSendsThePendingKill(t *testing.T) {
	s, sock := newRunningServer(t)
	s.keepChildren = true
	cl := dial(t, sock)
	cl.call(spawnReqArgs(t, 1, "fk", "sleep", "30"))
	cl.call(spawnReqArgs(t, 2, "kept", "sleep", "30"))
	cl.call(authed(`{"jsonrpc":"2.0","id":3,"method":"process.kill","params":{"id":"fk","signal":"SIGTERM","farewell":"aGkK","graceMs":20000}}`))
	s.stopChildren()
	if kb := exitFrameOfKilledBy(cl.waitExit("fk")); kb != "client" {
		t.Errorf("exit killedBy = %q, want client", kb)
	}
	if !s.procs.get("kept").isRunning() {
		t.Error("-keep-children ended a child with no farewell")
	}
}

// The grace timer that fires while the -keep-children shutdown signals a pending
// farewell sends no second signal (claustrum's own). The seam fires the timer
// between the read of the farewell and the signal of the sweep.
func TestFarewellSweepAndGraceSignalOnce(t *testing.T) {
	s, sock := newRunningServer(t)
	cl := dial(t, sock)
	cl.call(spawnReqArgs(t, 1, "fo", "sleep", "30"))
	cl.call(authed(`{"jsonrpc":"2.0","id":2,"method":"process.kill","params":{"id":"fo","signal":"SIGTERM","farewell":"aGkK","graceMs":20000}}`))
	// The stub counts the signals to the child and delivers none, so the child
	// lives on and no reap can drop a second signal before it is counted.
	p := s.procs.get("fo")
	realSignal := signalGroup
	var signals atomic.Int32
	signalGroup = func(g *procGroup, proc *os.Process, signame string) error {
		if proc == p.cmd.Process {
			signals.Add(1)
			return nil
		}
		return realSignal(g, proc, signame)
	}
	t.Cleanup(func() { signalGroup = realSignal })
	farewellSweepHook = func(h *farewellHold) {
		h.timer.Reset(0)
		// Give the timer up to 300 ms to signal. With the fix it cannot, because
		// the sweep holds p.mu here.
		deadline := time.Now().Add(300 * time.Millisecond)
		for signals.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(2 * time.Millisecond)
		}
	}
	t.Cleanup(func() { farewellSweepHook = nil })
	s.procs.endPendingFarewells()
	if n := signals.Load(); n != 1 {
		t.Errorf("signals to the child = %d, want 1", n)
	}
	signalGroup = realSignal
	_ = realSignal(p.group, p.cmd.Process, "KILL")
	if kb := exitFrameOfKilledBy(cl.waitExit("fo")); kb != "client" {
		t.Errorf("exit killedBy = %q, want client", kb)
	}
}

// A request with no farewell gets no farewellEnd member.
func TestKillAndWaitWithoutFarewellHasNoEnd(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	cl.call(spawnReqArgs(t, 1, "nf", "sleep", "30"))
	if got, want := string(cl.call(authed(`{"jsonrpc":"2.0","id":2,"method":"process.killAndWait","params":{"id":"nf"}}`))), replyLine(2, `{"found":true,"died":true}`); got != want {
		t.Errorf("reply = %s, want %s", got, want)
	}
	if got, want := string(cl.call(authed(`{"jsonrpc":"2.0","id":3,"method":"process.killAndWait","params":{"id":"nope"}}`))), replyLine(3, `{"found":false,"died":false}`); got != want {
		t.Errorf("reply = %s, want %s", got, want)
	}
}
