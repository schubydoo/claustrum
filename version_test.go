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

// A `go install pkg@vX.Y.Z` build embeds no vcs.* settings — it builds from the
// module cache, which has no VCS context — and records the resolved version in
// BuildInfo.Main.Version instead. With no settings to stamp from, that module
// version must replace the dev sentinel.
func TestApplyModuleVersionFillsFromMainVersion(t *testing.T) {
	oldV, oldT := Version, BuildTime
	defer func() { Version, BuildTime = oldV, oldT }()
	Version = devSentinel
	applyVCSStamp(nil) // the `go install` case: no vcs.* settings at all
	applyModuleVersion("v1.7.1")
	if Version != "v1.7.1" {
		t.Errorf("Version = %q, want v1.7.1", Version)
	}
	if BuildTime != "unknown" {
		t.Errorf("BuildTime = %q, want it left at unknown (module cache has no commit time)", BuildTime)
	}
}

// Precedence: ldflags > vcs.revision > Main.Version. Anything already stamped
// into Version — by -ldflags or by applyVCSStamp — outranks the module version.
func TestApplyModuleVersionRespectsPrecedence(t *testing.T) {
	oldV, oldT := Version, BuildTime
	defer func() { Version, BuildTime = oldV, oldT }()

	Version = "v1.2.3" // as if injected via -ldflags
	applyModuleVersion("v9.9.9")
	if Version != "v1.2.3" {
		t.Errorf("applyModuleVersion overrode an ldflags-injected version: %s", Version)
	}

	Version = devSentinel
	applyVCSStamp([]debug.BuildSetting{{Key: "vcs.revision", Value: "abc123"}})
	applyModuleVersion("v9.9.9")
	if Version != "abc123" {
		t.Errorf("applyModuleVersion overrode a vcs.revision stamp: %s", Version)
	}
}

// Main.Version holds "(devel)" for a local build and is empty when the binary
// carries no module info; neither is more informative than the sentinel.
func TestApplyModuleVersionRejectsNonVersions(t *testing.T) {
	oldV := Version
	defer func() { Version = oldV }()
	for _, mainVersion := range []string{"(devel)", ""} {
		Version = devSentinel
		applyModuleVersion(mainVersion)
		if Version != devSentinel {
			t.Errorf("applyModuleVersion(%q) = %q, want the %q sentinel kept", mainVersion, Version, devSentinel)
		}
	}
}

func TestGzipErrPrefix(t *testing.T) {
	if got := (gzipErr{errors.New("invalid header")}).Error(); got != "gzip: invalid header" {
		t.Errorf("gzipErr.Error() = %q, want %q", got, "gzip: invalid header")
	}
}
