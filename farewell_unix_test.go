//go:build unix

package main

import (
	"testing"
	"time"
)

// process.kill with a farewell on a child that does not read it: the SIGTERM
// comes after graceMs (measured on Linux and macOS, build 52d82f0a).
func TestFarewellKillSignalsAfterTheGrace(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	cl.call(spawnReqArgs(t, 1, "fw2", "sleep", "30"))
	start := time.Now()
	if got, want := string(cl.call(authed(`{"jsonrpc":"2.0","id":2,"method":"process.kill","params":{"id":"fw2","signal":"SIGTERM","farewell":"aGkK","graceMs":500}}`))), replyLine(2, `{"success":true,"farewellEnd":"queued"}`); got != want {
		t.Fatalf("reply = %s, want %s", got, want)
	}
	frames := cl.waitExit("fw2")
	if d := time.Since(start); d < 450*time.Millisecond {
		t.Errorf("exit after %v, want the grace of 500 ms first", d)
	}
	if got, want := exitFrameOf(t, frames), `{"type":"stream","processId":"fw2","stream":"exit","seq":1,"exitCode":-1,"signal":"SIGTERM","killedBy":"client"}`; got != want {
		t.Errorf("exit frame = %s, want %s", got, want)
	}
}

// A farewell on a record that a plain kill already signalled gives unsent
// (measured on Linux and macOS, build 52d82f0a). The child ignores SIGTERM, so the
// record is still live at the second kill.
func TestFarewellKillOnSignalledRecordIsUnsent(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	spawnIgnoreTerm(t, cl, "fw3", 1)
	cl.call(authed(`{"jsonrpc":"2.0","id":2,"method":"process.kill","params":{"id":"fw3","signal":"SIGTERM"}}`))
	if got, want := string(cl.call(authed(`{"jsonrpc":"2.0","id":3,"method":"process.kill","params":{"id":"fw3","signal":"SIGTERM","farewell":"aGkK","graceMs":100}}`))), replyLine(3, `{"success":true,"farewellEnd":"unsent"}`); got != want {
		t.Errorf("reply = %s, want %s", got, want)
	}
	cl.call(authed(`{"jsonrpc":"2.0","id":4,"method":"process.kill","params":{"id":"fw3","signal":"KILL"}}`))
	cl.waitExit("fw3")
}

// killAndWait with a farewell on a child that ignores SIGTERM: the grace runs out,
// then the SIGTERM, its timeoutMs and the SIGKILL. The reply comes after both
// waits, with escalated and expired (measured on Linux and macOS, build 52d82f0a).
func TestFarewellKillAndWaitExpired(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	spawnIgnoreTerm(t, cl, "fw6", 1)
	start := time.Now()
	got := string(cl.call(authed(`{"jsonrpc":"2.0","id":2,"method":"process.killAndWait","params":{"id":"fw6","signal":"SIGTERM","timeoutMs":300,"farewell":"aGkK","graceMs":300}}`)))
	if want := replyLine(2, `{"found":true,"died":true,"escalated":true,"farewellEnd":"expired"}`); got != want {
		t.Errorf("reply = %s, want %s", got, want)
	}
	if d := time.Since(start); d < 550*time.Millisecond {
		t.Errorf("reply after %v, want the grace and the timeout first (600 ms)", d)
	}
	if f := cl.waitExit("fw6"); exitFrameOf(t, f) != `{"type":"stream","processId":"fw6","stream":"exit","seq":2,"exitCode":-1,"signal":"SIGKILL","killedBy":"client"}` {
		t.Errorf("exit frame = %s", exitFrameOf(t, f))
	}
}
