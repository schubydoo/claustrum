package main

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestParseLogLevel(t *testing.T) {
	cases := map[string]logLevel{
		"":          logLevelDebug, // empty -> emit everything
		"debug":     logLevelDebug,
		"DEBUG":     logLevelDebug,
		"  info  ":  logLevelInfo, // trimmed + lowercased
		"info":      logLevelInfo,
		"warn":      logLevelWarn,
		"warning":   logLevelWarn,
		"error":     logLevelError,
		"nonsense":  logLevelDebug, // unknown -> never silently hide logs
		"INFOLEVEL": logLevelDebug,
	}
	for in, want := range cases {
		if got := parseLogLevel(in); got != want {
			t.Errorf("parseLogLevel(%q) = %d, want %d", in, got, want)
		}
	}
}

// captureLog redirects the stdlib default logger into a buffer for the duration
// of fn, mirroring how the other suites assert on log output.
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

func withThreshold(t *testing.T, level logLevel) {
	t.Helper()
	old := logThreshold.Load()
	logThreshold.Store(int32(level))
	t.Cleanup(func() { logThreshold.Store(old) })
}

func TestLogThresholdFilters(t *testing.T) {
	withThreshold(t, logLevelWarn)
	out := captureLog(t, func() {
		logDebugf("[Server] debug line")
		logInfof("[Server] info line")
		logWarnf("[Server] warn line")
		logErrorf("[Server] error line")
	})
	if strings.Contains(out, "debug line") || strings.Contains(out, "info line") {
		t.Errorf("levels below threshold leaked: %q", out)
	}
	if !strings.Contains(out, "warn line") || !strings.Contains(out, "error line") {
		t.Errorf("levels at/above threshold dropped: %q", out)
	}
}

func TestLogDefaultThresholdEmitsEverything(t *testing.T) {
	withThreshold(t, logLevelDebug) // the daemon's default
	out := captureLog(t, func() {
		logDebugf("[shellenv] d")
		logInfof("[Server] i")
		logWarnf("[frameSink] w")
		logErrorf("[process.Manager] e")
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
func TestLogTagPrecedesPrefixIntact(t *testing.T) {
	withThreshold(t, logLevelDebug)
	out := captureLog(t, func() { logInfof("[Server] New connection from: %s", "x") })
	if !strings.HasPrefix(out, "INFO ") {
		t.Errorf("level tag missing at line start: %q", out)
	}
	if !strings.Contains(out, "[Server] New connection from: x") {
		t.Errorf("component prefix not byte-intact: %q", out)
	}
}
