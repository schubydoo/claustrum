package files

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// files.extract_tar must refuse a destDir that is (or contains) the home
// directory, BEFORE any filesystem effect.
//
// Safe to run against the unfixed tree, twice over: the wipe is stubbed, so the
// recursive delete cannot fire, and the home the rows name is a t.TempDir() rather
// than the developer's real one. That is this repo's rule for guard tests — a test
// whose failure mode is destroying the machine is not a test — and it is also how
// the incident this guards against would have been caught: the fuzzer that fired
// it ran unstubbed, against a live daemon, with the real home reachable.
//
// The archive is DELIBERATELY nonexistent on every refused row. The guard runs
// before the archive is opened, so with it holding the row is unaffected; if it
// regresses, extraction fails on the missing archive instead of unpacking into a
// home directory. Same construction as the "root destDir" row in
// TestFilesExtractTarErrors, for the same reason.
func TestFilesExtractTarRefusesHomeDir(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home", "someone")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(homeEnvVar(), home)

	wiped := ""
	oldWipe := wipeDestDir
	wipeDestDir = func(_ *os.Root, name string) error { wiped = name; return nil }
	t.Cleanup(func() { wipeDestDir = oldWipe })

	s := newTestServer(t)
	archive := filepath.Join(t.TempDir(), "no-such.tar.gz")

	for _, tc := range []struct {
		name, destDir string
	}{
		// The incident's literal input. It also proves the wiring end to end:
		// nothing in filesExtractTar mentions "~", so this row only refuses if
		// bindParams' expansion and the guard meet.
		{"bare tilde", "~"},
		{"the home directory spelled out", home},
		{"the parent of home", filepath.Dir(home)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wiped = ""
			got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar",
				map[string]any{"archivePath": archive, "destDir": tc.destDir}))
			if wiped != "" {
				t.Errorf("destDir %q was refused but the wipe still ran on %q — the guard "+
					"must reject before any filesystem effect", tc.destDir, wiped)
			}
			if !strings.Contains(got, "destDir must not be or contain the home directory") {
				t.Errorf("extract into %q = %s, want the home-directory refusal", tc.destDir, got)
			}
		})
	}

	// The control. A directory UNDER home must still be accepted, or the guard has
	// broken the daemon's own install path instead of protecting it. It gets past
	// the guard and fails on the missing archive, which is the proof it was not
	// refused.
	t.Run("a directory under home is accepted", func(t *testing.T) {
		wiped = ""
		got := dispatchRaw(t, s, rpcLine(t, "files.extract_tar",
			map[string]any{"archivePath": archive, "destDir": filepath.Join(home, ".claude", "1.0.0")}))
		if strings.Contains(got, "home directory") {
			t.Errorf("extract under home = %s, want it accepted — ~/... is the primary real use", got)
		}
		if !strings.Contains(got, "open archive") {
			t.Errorf("extract under home = %s, want it to reach the archive open", got)
		}
	})
}
