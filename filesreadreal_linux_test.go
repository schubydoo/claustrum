package main

import (
	"errors"
	"slices"
	"testing"

	"github.com/schubydoo/claustrum/internal/files"
)

// stubStartTest replaces the reads of the start test of internal/files and
// resets its cached answer, before and after the test, so no test depends on
// the /proc of the host.
func stubStartTest(t *testing.T, link func(string) (string, error), read func(string) ([]byte, error)) {
	t.Helper()
	t.Cleanup(files.SetStartTestForTest(link, read))
}

func okLink(string) (string, error) { return "/", nil }
func okInfo(string) ([]byte, error) { return []byte("pos:\t0\nmnt_id:\t29\n"), nil }
func noLink(string) (string, error) { return "", errors.New("no") }

// TestCapabilitiesListFilesReadRealOnlyAfterAPassedStartTest: the features list has
// files.read.real after a passed start test and drops only that name after a failed
// one (the exact start test is claustrum's own).
func TestCapabilitiesListFilesReadRealOnlyAfterAPassedStartTest(t *testing.T) {
	// Registered first, so it runs after the restores of stubStartTest: the
	// start test must answer as it did before this test.
	before := files.ReadRealAvailable()
	t.Cleanup(func() {
		if got := files.ReadRealAvailable(); got != before {
			t.Errorf("after the test: files.ReadRealAvailable() = %t, want %t", got, before)
		}
	})
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
