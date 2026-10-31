package main

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
)

// stubStartTest replaces the reads of the start test and resets its cached
// answer, before and after the test, so no test depends on the /proc of the host.
func stubStartTest(t *testing.T, link func(string) (string, error), read func(string) ([]byte, error)) {
	t.Helper()
	oldL, oldR := startReadlink, startReadFile
	startReadlink, startReadFile = link, read
	filesRealOnce = sync.Once{}
	t.Cleanup(func() {
		startReadlink, startReadFile = oldL, oldR
		filesRealOnce = sync.Once{}
	})
}

func okLink(string) (string, error)  { return "/", nil }
func okInfo(string) ([]byte, error)  { return []byte("pos:\t0\nmnt_id:\t29\n"), nil }
func noLink(string) (string, error)  { return "", errors.New("no") }
func noInfo(string) ([]byte, error)  { return nil, errors.New("no") }
func noMntID(string) ([]byte, error) { return []byte("pos:\t0\n"), nil }

// TestStartTestFailLogsFallbackOnce: a failing start test logs the fallback line
// once and files.read gives no real (build 683cc6a9, tmpfs /proc with only self).
func TestStartTestFailLogsFallbackOnce(t *testing.T) {
	for name, tc := range map[string]struct {
		link func(string) (string, error)
		read func(string) ([]byte, error)
	}{"no fd link": {noLink, okInfo}, "no fdinfo": {okLink, noInfo}, "no mnt_id": {okLink, noMntID}} {
		t.Run(name, func(t *testing.T) {
			stubStartTest(t, tc.link, tc.read)
			out := captureLog(t, logFilesReadRealStart)
			if n := strings.Count(out, filesRealFailLine); n != 1 || strings.Contains(out, filesRealPassLine) {
				t.Errorf("log = %q, want the fallback line once", out)
			}
			if filesReadRealAvailable() {
				t.Error("filesReadRealAvailable() = true after a failed test")
			}
			root := resolveTestRoot(t, t.TempDir())
			writeFile(t, root+"/f", "x", 0o644)
			got := readRealCalls(t, root, []map[string]any{{"path": root + "/f", "real": true}})[0]
			if strings.Contains(string(got), `"real"`) {
				t.Errorf("reply = %s, want no real member", got)
			}
		})
	}
}

// TestStartTestPassLogsOnce: a passing start test logs the pass line once.
func TestStartTestPassLogsOnce(t *testing.T) {
	stubStartTest(t, okLink, okInfo)
	out := captureLog(t, logFilesReadRealStart)
	if n := strings.Count(out, filesRealPassLine); n != 1 || strings.Contains(out, filesRealFailLine) {
		t.Errorf("log = %q, want the pass line once", out)
	}
	if !filesReadRealAvailable() {
		t.Error("filesReadRealAvailable() = false after a good test")
	}
}

// TestCapabilitiesListFilesReadRealOnlyAfterAPassedStartTest: the features list has
// files.read.real after a passed start test and drops only that name after a failed
// one (the exact start test is claustrum's own).
func TestCapabilitiesListFilesReadRealOnlyAfterAPassedStartTest(t *testing.T) {
	s := &server{}
	all := slices.Clone(capabilityFeatures)
	less := slices.DeleteFunc(slices.Clone(all), func(n string) bool { return n == filesReadRealFeature })
	stubStartTest(t, okLink, okInfo)
	if got := s.featuresFor(); !slices.Equal(got, all) {
		t.Errorf("passed start test: features = %v, want %v", got, all)
	}
	stubStartTest(t, noLink, okInfo)
	if got := s.featuresFor(); !slices.Equal(got, less) {
		t.Errorf("failed start test: features = %v, want %v", got, less)
	}
	// The drop works on a copy: the list of the next reply is whole again.
	if !slices.Equal(capabilityFeatures, all) {
		t.Errorf("capabilityFeatures changed to %v, want %v", capabilityFeatures, all)
	}
}
