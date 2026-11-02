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

// On the `go install pkg@vX.Y.Z` path there is no vcs.time and no -ldflags, so
// the stamp baked into the tagged source (buildstamp.go) is the only build time
// available. It applies when the resolved module version is exactly the release
// the stamp describes.
func TestApplyReleaseStampFillsBuildTime(t *testing.T) {
	oldV, oldT := Version, BuildTime
	defer func() { Version, BuildTime = oldV, oldT }()
	Version, BuildTime = devSentinel, unknownTime
	applyReleaseStamp("v1.7.3", "1.7.3", "2026-07-25T21:04:16Z")
	if BuildTime != "2026-07-25T21:04:16Z" {
		t.Errorf("BuildTime = %q, want the baked release stamp", BuildTime)
	}
}

// The stamp must never be claimed by a build that isn't that release. A
// `go install …@main` / …@<sha> build resolves to a pseudo-version, which must
// keep unknownTime rather than inherit the previous release's timestamp.
func TestApplyReleaseStampRejectsPseudoVersion(t *testing.T) {
	oldV, oldT := Version, BuildTime
	defer func() { Version, BuildTime = oldV, oldT }()
	for _, mainVersion := range []string{
		"v1.7.4-0.20260725210416-b7ca7395d3e2", // …@main, after the 1.7.3 release
		"v1.7.2",                               // an older tag than the stamp
		"1.7.3",                                // missing the "v" the module path uses
	} {
		Version, BuildTime = devSentinel, unknownTime
		applyReleaseStamp(mainVersion, "1.7.3", "2026-07-25T21:04:16Z")
		if BuildTime != unknownTime {
			t.Errorf("applyReleaseStamp(%q, …) set BuildTime = %q, want it left unknown",
				mainVersion, BuildTime)
		}
	}
}

// Precedence: an -ldflags stamp (goreleaser) and a vcs.time stamp (local git
// build) both outrank the baked release stamp, and empty stamp fields — the
// state of buildstamp.go between releases — mean "no stamp", never a guess.
func TestApplyReleaseStampRespectsPrecedence(t *testing.T) {
	oldV, oldT := Version, BuildTime
	defer func() { Version, BuildTime = oldV, oldT }()

	Version, BuildTime = devSentinel, "2026-01-01T00:00:00Z" // as if -ldflags/vcs.time set it
	applyReleaseStamp("v1.7.3", "1.7.3", "2026-07-25T21:04:16Z")
	if BuildTime != "2026-01-01T00:00:00Z" {
		t.Errorf("applyReleaseStamp overrode an existing build time: %s", BuildTime)
	}

	for _, stamp := range [][2]string{{"", "2026-07-25T21:04:16Z"}, {"1.7.3", ""}, {"", ""}} {
		Version, BuildTime = devSentinel, unknownTime
		applyReleaseStamp("v1.7.3", stamp[0], stamp[1])
		if BuildTime != unknownTime {
			t.Errorf("applyReleaseStamp with empty stamp %q/%q set BuildTime = %q, want unknown",
				stamp[0], stamp[1], BuildTime)
		}
	}
}

// applyModuleVersion drives the stamp, so the whole `go install` fallback — the
// version from module info, the build time from the baked stamp — lands in one
// call. The generated buildstamp.go is empty on an unreleased tree, so this
// drives the wiring with the real consts and asserts only what holds either way.
func TestApplyModuleVersionAppliesReleaseStamp(t *testing.T) {
	oldV, oldT := Version, BuildTime
	defer func() { Version, BuildTime = oldV, oldT }()
	Version, BuildTime = devSentinel, unknownTime
	applyModuleVersion("v" + releaseVersion)
	if releaseVersion == "" || releaseTime == "" {
		if BuildTime != unknownTime {
			t.Errorf("BuildTime = %q with no stamp baked in, want unknown", BuildTime)
		}
		return
	}
	if BuildTime != releaseTime {
		t.Errorf("BuildTime = %q, want the baked stamp %q", BuildTime, releaseTime)
	}
}

func TestGzipErrPrefix(t *testing.T) {
	if got := (gzipErr{errors.New("invalid header")}).Error(); got != "gzip: invalid header" {
		t.Errorf("gzipErr.Error() = %q, want %q", got, "gzip: invalid header")
	}
}
