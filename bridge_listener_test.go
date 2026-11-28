package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// TestOwnerTestLevel: no variable, 0 and an empty value turn the check off, 1 is
// loose, and 2, strict, yes and on are strict (13047a51, measured on Linux,
// macOS and Windows). Off, false, no, true, 3, " 1" and 00 are strict too
// (13047a51, measured on Linux).
func TestOwnerTestLevel(t *testing.T) {
	for _, tc := range []struct {
		value string
		set   bool
		want  int
	}{
		{"", false, ownerTestNone},
		{"", true, ownerTestNone},
		{"0", true, ownerTestNone},
		{"1", true, ownerTestLenient},
		{"2", true, ownerTestFirm},
		{"strict", true, ownerTestFirm},
		{"yes", true, ownerTestFirm},
		{"on", true, ownerTestFirm},
		{"off", true, ownerTestFirm},
		{"false", true, ownerTestFirm},
		{"no", true, ownerTestFirm},
		{"true", true, ownerTestFirm},
		{"3", true, ownerTestFirm},
		{" 1", true, ownerTestFirm},
		{"00", true, ownerTestFirm},
	} {
		if got := ownerTestLevel(tc.value, tc.set); got != tc.want {
			t.Errorf("ownerTestLevel(%q, %v) = %d, want %d", tc.value, tc.set, got, tc.want)
		}
	}
}

// stubListenerAnswer makes the check answer answer.
func stubListenerAnswer(t *testing.T, answer string) {
	t.Helper()
	old := listenerAnswerOf
	listenerAnswerOf = func(net.Conn) (string, *ownerRejection) { return answer, nil }
	t.Cleanup(func() { listenerAnswerOf = old })
}

// TestJudgeSocketOwnerTexts: the pass line and the too_weak refusal in the texts of
// 13047a51 with the program name of claustrum (measured on macOS and Windows).
func TestJudgeSocketOwnerTexts(t *testing.T) {
	stubListenerAnswer(t, listenerAnswerUnavailable)
	var stderr bytes.Buffer
	if err := judgeSocketOwner(nil, "1", true, &stderr); err != nil {
		t.Fatalf("value 1: %v, want a pass", err)
	}
	if got, want := stderr.String(), "claustrum: listener check (unavailable)\n"; got != want {
		t.Errorf("value 1: stderr %q, want %q", got, want)
	}
	for _, value := range []string{"2", "strict", "yes", "on"} {
		stderr.Reset()
		err := judgeSocketOwner(nil, value, true, &stderr)
		const want = `refusing the listener (too_weak): here the check can say no more than "unavailable"; nothing was sent to it`
		if err == nil || err.Error() != want {
			t.Errorf("value %q: %v, want %q", value, err, want)
		}
		if stderr.Len() != 0 {
			t.Errorf("value %q: stderr %q, want none", value, stderr.String())
		}
	}
	for _, value := range []string{"", "0"} {
		stderr.Reset()
		if err := judgeSocketOwner(nil, value, true, &stderr); err != nil || stderr.Len() != 0 {
			t.Errorf("value %q: %v and stderr %q, want no check", value, err, stderr.String())
		}
	}
	stubListenerAnswer(t, listenerAnswerOn)
	stderr.Reset()
	if err := judgeSocketOwner(nil, "strict", true, &stderr); err != nil {
		t.Fatalf("strict with the answer on: %v, want a pass", err)
	}
	if got, want := stderr.String(), "claustrum: listener check (on)\n"; got != want {
		t.Errorf("strict with the answer on: stderr %q, want %q", got, want)
	}
}

// fakeListener listens on a new socket in a folder with a daemon.token of token,
// and returns its path and a channel with the bytes that the first connection got
// before its close.
func fakeListener(t *testing.T, token string) (string, <-chan []byte) {
	t.Helper()
	sock := adoptSocketDir(t, token)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	got := make(chan []byte, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			got <- nil
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		b, _ := io.ReadAll(c)
		got <- b
	}()
	return sock, got
}

// TestBridgeRefusesTheListenerBeforeItSends: a strict check that gets the answer
// unavailable ends the bridge with the refusal, and the listener gets nothing
// (13047a51, measured on macOS). main exits 79 (measured on macOS and Windows).
// The folder holds a daemon.token, so the bridge has an adopt frame to send, and
// the check comes before it.
func TestBridgeRefusesTheListenerBeforeItSends(t *testing.T) {
	stubListenerAnswer(t, listenerAnswerUnavailable)
	t.Setenv(bridgeListenerEnv, "strict")
	t.Setenv(bridgeAdoptEnv, "1")
	sock, got := fakeListener(t, "test-token-0123")
	if bridgeAdoptFrame(sock, []string{"A=1"}) == nil {
		t.Fatal("the folder gives no adopt frame, so the test cannot see the order")
	}
	err := runBridge(sock)
	var refused *ownerRejection
	if !errors.As(err, &refused) || refused.kind != "too_weak" {
		t.Fatalf("runBridge = %v, want the too_weak refusal", err)
	}
	if b := <-got; len(b) != 0 {
		t.Errorf("the listener got %q, want nothing", b)
	}

	sock, _ = fakeListener(t, "")
	if code, exited := runMain(t, "-bridge", "-socket", sock); !exited || code != 79 {
		t.Errorf("main -bridge with a refused listener: exit %d (exited %v), want 79", code, exited)
	}
}

// refusedRedialRun runs main -bridge with the check value 1 and CLAUDE_SSH_ADOPT_ENV=1.
// The stub passes each check before call refuseAt and refuses that call. The fake
// listener closes each connection before it at the adopt frame, with no reply, so the
// bridge dials again at once. It returns the exit, stderr, the count of checks and the
// bytes of the refused connection.
func refusedRedialRun(t *testing.T, refuseAt int32) (code int, exited bool, stderr string, calls int32, refused []byte) {
	t.Helper()
	var n atomic.Int32
	old := listenerAnswerOf
	listenerAnswerOf = func(net.Conn) (string, *ownerRejection) {
		if n.Add(1) < refuseAt {
			return listenerAnswerOn, nil
		}
		return "", &ownerRejection{kind: "other_user", detail: "user 4242 listens"}
	}
	t.Cleanup(func() { listenerAnswerOf = old })
	t.Setenv(bridgeListenerEnv, "1")
	t.Setenv(bridgeAdoptEnv, "1")
	sock := adoptSocketDir(t, "test-token-0123")
	if bridgeAdoptFrame(sock, os.Environ()) == nil {
		t.Fatal("the folder gives no adopt frame, so the bridge never dials again")
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	last := make(chan []byte, 1)
	go func() {
		for i := int32(1); i < refuseAt; i++ {
			c, err := ln.Accept()
			if err != nil {
				last <- nil
				return
			}
			_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, _ = bufio.NewReader(c).ReadString('\n')
			_ = c.Close()
		}
		c, err := ln.Accept()
		// A bridge that wrongly goes on dials once more, and that dial then fails.
		_ = ln.Close()
		if err != nil {
			last <- nil
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		b, _ := io.ReadAll(c)
		last <- b
	}()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	t.Cleanup(func() {
		os.Stderr = oldStderr
		_ = w.Close()
		_ = r.Close()
	})
	printed := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r)
		printed <- b
	}()
	os.Stderr = w
	code, exited = runMain(t, "-bridge", "-socket", sock)
	os.Stderr = oldStderr
	_ = w.Close()
	return code, exited, string(<-printed), n.Load(), <-last
}

// TestBridgeChecksTheListenerOfARedial: the listener of the first connection passes
// and closes at the adopt frame, so the bridge dials again. The listener of that
// redial is refused before the bridge writes on it (claustrum's own, not measured).
// main prints the refusal line and exits 79, and the second listener gets nothing.
func TestBridgeChecksTheListenerOfARedial(t *testing.T) {
	code, exited, stderr, calls, got := refusedRedialRun(t, 2)
	if !exited || code != ownerRejectStatus {
		t.Errorf("main -bridge with a refused redial: exit %d (exited %v), want 79", code, exited)
	}
	const want = "claustrum: listener check (on)\n" +
		"claustrum: refusing the listener (other_user): user 4242 listens; nothing was sent to it\n"
	if stderr != want {
		t.Errorf("stderr %q, want %q", stderr, want)
	}
	if calls != 2 {
		t.Errorf("the check ran %d times, want 2", calls)
	}
	if len(got) != 0 {
		t.Errorf("the listener of the redial got %d bytes, want nothing", len(got))
	}
}

// TestBridgeChecksTheListenerOfTheRelayConnection: both frame connections close with
// no reply, so the bridge dials a third time to relay with no frame. That listener is
// refused before the relay starts (claustrum's own, not measured). main exits 79, and
// the third listener gets nothing.
func TestBridgeChecksTheListenerOfTheRelayConnection(t *testing.T) {
	code, exited, stderr, calls, got := refusedRedialRun(t, 3)
	if !exited || code != ownerRejectStatus {
		t.Errorf("main -bridge with a refused relay connection: exit %d (exited %v), want 79", code, exited)
	}
	const want = "claustrum: listener check (on)\n" +
		"claustrum: refusing the listener (other_user): user 4242 listens; nothing was sent to it\n"
	if stderr != want {
		t.Errorf("stderr %q, want %q", stderr, want)
	}
	if calls != 3 {
		t.Errorf("the check ran %d times, want 3", calls)
	}
	if len(got) != 0 {
		t.Errorf("the listener of the relay connection got %d bytes, want nothing", len(got))
	}
}
