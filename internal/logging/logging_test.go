package logging

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestParseLogLevel(t *testing.T) {
	cases := map[string]Level{
		"":          LevelDebug, // empty -> emit everything
		"debug":     LevelDebug,
		"DEBUG":     LevelDebug,
		"  info  ":  LevelInfo, // trimmed + lowercased
		"info":      LevelInfo,
		"warn":      LevelWarn,
		"warning":   LevelWarn,
		"error":     LevelError,
		"nonsense":  LevelDebug, // unknown -> never silently hide logs
		"INFOLEVEL": LevelDebug,
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %d, want %d", in, got, want)
		}
	}
}

// captureLog redirects the stdlib default logger into a buffer for the duration
// of fn.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldW); log.SetFlags(oldF) })
	fn()
	return buf.String()
}

func withThreshold(t *testing.T, level Level) {
	t.Helper()
	old := Threshold.Load()
	Threshold.Store(int32(level))
	t.Cleanup(func() { Threshold.Store(old) })
}

func TestLogThresholdFilters(t *testing.T) {
	withThreshold(t, LevelWarn)
	out := captureLog(t, func() {
		Debugf("[Server] debug line")
		Infof("[Server] info line")
		Warnf("[Server] warn line")
		Errorf("[Server] error line")
	})
	if strings.Contains(out, "debug line") || strings.Contains(out, "info line") {
		t.Errorf("levels below threshold leaked: %q", out)
	}
	if !strings.Contains(out, "warn line") || !strings.Contains(out, "error line") {
		t.Errorf("levels at/above threshold dropped: %q", out)
	}
}

func TestLogDefaultThresholdEmitsEverything(t *testing.T) {
	withThreshold(t, LevelDebug) // the daemon's default
	out := captureLog(t, func() {
		Debugf("[shellenv] d")
		Infof("[Server] i")
		Warnf("[frameSink] w")
		Errorf("[process.Manager] e")
	})
	for _, want := range []string{"[shellenv] d", "[Server] i", "[frameSink] w", "[process.Manager] e"} {
		if !strings.Contains(out, want) {
			t.Errorf("default threshold dropped %q; got %q", want, out)
		}
	}
}

// The level tag must precede the "[Component]" prefix and leave it byte-intact,
// so anything that greps for the prefix keeps working. (Tags are padded to a
// fixed width, so a 4-letter level like INFO is followed by two spaces before
// the prefix — that column-aligns "[" across every level.)
//
// The test checks only the line it wrote.
func TestLogTagPrecedesPrefixIntact(t *testing.T) {
	withThreshold(t, LevelDebug)
	out := captureLog(t, func() { Infof("[Server] New connection from: %s", "x") })
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "[Server] New connection from: x") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("component prefix not byte-intact: %q", out)
	}
	if !strings.HasPrefix(line, "INFO ") {
		t.Errorf("level tag missing at line start: %q", line)
	}
}
