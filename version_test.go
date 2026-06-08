package main

import (
	"errors"
	"runtime/debug"
	"testing"
)

func TestResolveVersionKeepsInjected(t *testing.T) {
	old := Version
	defer func() { Version = old }()
	Version = "v9.9.9"
	resolveVersion()
	if Version != "v9.9.9" {
		t.Errorf("resolveVersion overrode an ldflags-injected version: %s", Version)
	}
}

func TestResolveVersionFromBuildInfo(t *testing.T) {
	oldV, oldT := Version, BuildTime
	defer func() { Version, BuildTime = oldV, oldT }()
	Version = "claustrum-dev"
	// Reads debug.ReadBuildInfo(); vcs.* settings may be absent under `go test`,
	// in which case Version stays "claustrum-dev" — either way it must execute
	// without panicking and leave a non-empty version.
	resolveVersion()
	if Version == "" {
		t.Error("Version should be non-empty after resolveVersion")
	}
}

// applyVCSStamp must honor an ldflags-injected version: when Version differs from
// the dev sentinel it returns without consulting the VCS settings at all. Driving
// it with explicit settings (rather than the test binary's absent VCS metadata)
// makes the guard's effect observable.
func TestApplyVCSStampKeepsInjected(t *testing.T) {
	oldV, oldT := Version, BuildTime
	defer func() { Version, BuildTime = oldV, oldT }()
	Version = "v1.2.3"
	applyVCSStamp([]debug.BuildSetting{{Key: "vcs.revision", Value: "deadbeef"}})
	if Version != "v1.2.3" {
		t.Errorf("applyVCSStamp overrode an ldflags-injected version: %s", Version)
	}
}

// With the dev sentinel in place, applyVCSStamp copies vcs.revision/vcs.time into
// Version/BuildTime.
func TestApplyVCSStampFillsFromSettings(t *testing.T) {
	oldV, oldT := Version, BuildTime
	defer func() { Version, BuildTime = oldV, oldT }()
	Version = "claustrum-dev"
	applyVCSStamp([]debug.BuildSetting{
		{Key: "vcs.revision", Value: "abc123"},
		{Key: "vcs.time", Value: "2026-01-01T00:00:00Z"},
	})
	if Version != "abc123" {
		t.Errorf("Version = %q, want abc123", Version)
	}
	if BuildTime != "2026-01-01T00:00:00Z" {
		t.Errorf("BuildTime = %q, want the stamped vcs.time", BuildTime)
	}
}

func TestGzipErrPrefix(t *testing.T) {
	if got := (gzipErr{errors.New("invalid header")}).Error(); got != "gzip: invalid header" {
		t.Errorf("gzipErr.Error() = %q, want %q", got, "gzip: invalid header")
	}
}
