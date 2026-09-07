package main

import (
	"runtime/debug"
	"testing"
)

// TestResolveVersionWithoutBuildInfo pins the sentinel fallback: when
// debug.ReadBuildInfo reports !ok there is nothing to read, so Version and
// BuildTime must stay on the placeholders the package declares them with —
// "claustrum-dev" and "unknown", the exact strings -version would then print.
//
// The stub hands back a fully populated BuildInfo alongside ok=false on purpose.
// resolveVersion must honour the flag, not the payload: with a nil payload the
// only way to fail this would be a nil dereference, which asserts nothing about
// the fallback values.
func TestResolveVersionWithoutBuildInfo(t *testing.T) {
	populated := &debug.BuildInfo{
		Main: debug.Module{Version: "v9.9.9"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"},
			{Key: "vcs.time", Value: "2020-01-02T03:04:05Z"},
		},
	}

	for _, tc := range []struct {
		name          string
		ok            bool
		wantVersion   string
		wantBuildTime string
	}{
		{"no build info", false, devSentinel, unknownTime},
		{"build info present (control)", true, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "2020-01-02T03:04:05Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldV, oldB := Version, BuildTime
			oldRead := readBuildInfo
			t.Cleanup(func() { Version, BuildTime, readBuildInfo = oldV, oldB, oldRead })

			Version, BuildTime = devSentinel, unknownTime
			readBuildInfo = func() (*debug.BuildInfo, bool) { return populated, tc.ok }

			resolveVersion()
			if Version != tc.wantVersion {
				t.Errorf("Version = %q, want %q", Version, tc.wantVersion)
			}
			if BuildTime != tc.wantBuildTime {
				t.Errorf("BuildTime = %q, want %q", BuildTime, tc.wantBuildTime)
			}
		})
	}
}
