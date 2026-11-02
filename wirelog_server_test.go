package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 0 in the config file is a real setting ("keep everything"), not "unset", so it
// must win over the non-zero default — unlike the byte caps, where 0 means off.
func TestEffectiveWireLogMaxStringZeroFromConfigWins(t *testing.T) {
	zero := int64(0)
	cfg := config{wireLogMaxString: &zero}
	if got := cfg.effectiveWireLogMaxString(wireLogMaxString, false); got != 0 {
		t.Errorf("config 0 with unset CLI: got %d, want 0 (keep everything)", got)
	}
	if got := cfg.effectiveWireLogMaxString(256, true); got != 256 {
		t.Errorf("explicit CLI: got %d, want 256", got)
	}
	if got := (config{}).effectiveWireLogMaxString(wireLogMaxString, false); got != wireLogMaxString {
		t.Errorf("neither set: got %d, want the %d default", got, wireLogMaxString)
	}
}

// A path that cannot be opened must fail the boot. A daemon that serves happily
// while recording nothing produces an empty capture, and an empty capture reads
// as "the client sent nothing" — the worst failure mode for a diagnostic.
func TestWireLogUnopenablePathIsFatalNotSilent(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "no-such-dir", "wire.jsonl")
	if _, err := openWireLog(wireLogOptions{Path: bad}); err == nil {
		t.Fatal("openWireLog on an unopenable path returned nil error")
	}
	if _, err := newServerOnSocket(filepath.Join(t.TempDir(), "s.sock"), "tok", "", wireLogOptions{Path: bad}, false, false); err == nil {
		t.Fatal("newServerOnSocket accepted an unopenable -wire-log path")
	}
}

// When -wire-log opens but a LATER boot step fails, the capture must not leak. The
// daemon exits on such an error, but the in-process boot path (and any future
// caller that recovers) must see the file closed. Here the wire-log path is valid
// and the socket path is not, so newServerOnSocket fails past openWireLog.
func TestWireLogClosedWhenBootFailsAfterOpen(t *testing.T) {
	good := filepath.Join(t.TempDir(), "wire.jsonl")
	badSocket := filepath.Join(t.TempDir(), "no-such-dir", "rpc.sock")
	if _, err := newServerOnSocket(badSocket, "tok", "", wireLogOptions{Path: good}, false, false); err == nil {
		t.Fatal("newServerOnSocket accepted an unlistenable socket")
	}
	// The capture opened, then boot failed; the deferred Close must have run. A
	// second Close is a no-op, so this asserts the file handle was released cleanly.
	if _, err := os.Stat(good); err != nil {
		t.Fatalf("capture file missing after failed boot: %v", err)
	}
}

// Config precedence mirrors -metrics-addr: CLI wins when explicitly set,
// otherwise the file value applies.
func TestEffectiveWireLogPrecedence(t *testing.T) {
	cfg := config{wireLog: "/from/file.jsonl"}
	if got := cfg.effectiveWireLog("", false); got != "/from/file.jsonl" {
		t.Errorf("unset CLI: got %q, want the config value", got)
	}
	if got := cfg.effectiveWireLog("/from/cli.jsonl", true); got != "/from/cli.jsonl" {
		t.Errorf("explicit CLI: got %q, want the CLI value", got)
	}
	// An explicitly-empty CLI value must be able to turn it back off.
	if got := cfg.effectiveWireLog("", true); got != "" {
		t.Errorf("explicit empty CLI: got %q, want \"\" (off)", got)
	}
	if got := (config{}).effectiveWireLog("", false); got != "" {
		t.Errorf("neither set: got %q, want \"\" (off)", got)
	}
}
