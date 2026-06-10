package main

import (
	"log"
	"os"
	"strings"
	"sync/atomic"
)

// A tiny leveled logger layered over the stdlib log package. It exists so
// operators can quiet the daemon's diagnostics (CLAUSTRUM_LOG_LEVEL=warn, say)
// without us giving up the existing "[Component] message" log strings — the
// level tag is prepended *before* those prefixes, so anything that greps for
// "[Server]", "[process.Manager]", "[frameSink]", or "[shellenv]" keeps working.
//
// Output still flows through the stdlib default logger (log.Printf), so the
// standard timestamp prefix and log.SetOutput (used by tests) behave as before.

type logLevel int32

const (
	logLevelDebug logLevel = iota
	logLevelInfo
	logLevelWarn
	logLevelError
)

// logLevelTag is the fixed-width label printed ahead of each message. Padding
// keeps the "[Component]" prefixes column-aligned across levels.
var logLevelTag = [...]string{
	logLevelDebug: "DEBUG",
	logLevelInfo:  "INFO ",
	logLevelWarn:  "WARN ",
	logLevelError: "ERROR",
}

// logThreshold is the minimum level that gets emitted. Stored atomically so it
// can be read from any goroutine (it's only written once, at startup, today).
// It defaults to debug so the daemon stays as verbose as it was before levels
// existed; raising it drops everything below the chosen level.
var logThreshold atomic.Int32

func init() {
	logThreshold.Store(int32(parseLogLevel(os.Getenv("CLAUSTRUM_LOG_LEVEL"))))
}

// parseLogLevel maps an env value to a level. Empty, "debug", or any
// unrecognized value means "emit everything", so a typo never silently hides
// logs.
func parseLogLevel(s string) logLevel {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "info":
		return logLevelInfo
	case "warn", "warning":
		return logLevelWarn
	case "error":
		return logLevelError
	default: // "", "debug", or unknown
		return logLevelDebug
	}
}

func logEmit(level logLevel, format string, args ...any) {
	if level < logLevel(logThreshold.Load()) {
		return
	}
	log.Printf(logLevelTag[level]+" "+format, args...)
}

func logDebugf(format string, args ...any) { logEmit(logLevelDebug, format, args...) }
func logInfof(format string, args ...any)  { logEmit(logLevelInfo, format, args...) }
func logWarnf(format string, args ...any)  { logEmit(logLevelWarn, format, args...) }
func logErrorf(format string, args ...any) { logEmit(logLevelError, format, args...) }
