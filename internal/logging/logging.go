package logging

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

type Level int32

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// logLevelTag is the fixed-width label printed ahead of each message. Padding
// keeps the "[Component]" prefixes column-aligned across levels.
var logLevelTag = [...]string{
	LevelDebug: "DEBUG",
	LevelInfo:  "INFO ",
	LevelWarn:  "WARN ",
	LevelError: "ERROR",
}

// Threshold is the minimum level that gets emitted. Stored atomically so it
// can be read from any goroutine (it's only written once, at startup, today).
// It defaults to debug so the daemon stays as verbose as it was before levels
// existed; raising it drops everything below the chosen level.
var Threshold atomic.Int32

func init() {
	Threshold.Store(int32(ParseLevel(os.Getenv("CLAUSTRUM_LOG_LEVEL"))))
}

// ParseLevel maps an env value to a level. Empty, "debug", or any
// unrecognized value means "emit everything", so a typo never silently hides
// logs.
func ParseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "info":
		return LevelInfo
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	default: // "", "debug", or unknown
		return LevelDebug
	}
}

func logEmit(level Level, format string, args ...any) {
	if level < Level(Threshold.Load()) {
		return
	}
	log.Printf(logLevelTag[level]+" "+format, args...)
}

func Debugf(format string, args ...any) { logEmit(LevelDebug, format, args...) }
func Infof(format string, args ...any)  { logEmit(LevelInfo, format, args...) }
func Warnf(format string, args ...any)  { logEmit(LevelWarn, format, args...) }
func Errorf(format string, args ...any) { logEmit(LevelError, format, args...) }
