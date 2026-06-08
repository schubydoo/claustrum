package main

import (
	"errors"
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

func TestGzipErrPrefix(t *testing.T) {
	if got := (gzipErr{errors.New("invalid header")}).Error(); got != "gzip: invalid header" {
		t.Errorf("gzipErr.Error() = %q, want %q", got, "gzip: invalid header")
	}
}
