package main

import (
	"slices"
	"testing"
)

func TestReplaceOrAppendEnv(t *testing.T) {
	// Existing key is replaced in place.
	got := replaceOrAppendEnv([]string{"A=1", "B=2"}, "A", "9")
	if want := []string{"A=9", "B=2"}; !slices.Equal(got, want) {
		t.Errorf("replace: got %v, want %v", got, want)
	}

	// New key is appended.
	got = replaceOrAppendEnv([]string{"A=1"}, "C", "3")
	if want := []string{"A=1", "C=3"}; !slices.Equal(got, want) {
		t.Errorf("append: got %v, want %v", got, want)
	}

	// Prefix match must be exact ("A=" not a prefix of "AB=").
	got = replaceOrAppendEnv([]string{"AB=2"}, "A", "1")
	if want := []string{"AB=2", "A=1"}; !slices.Equal(got, want) {
		t.Errorf("prefix safety: got %v, want %v", got, want)
	}
}

func TestBuildEnvMergesOverEnviron(t *testing.T) {
	t.Setenv("CLAUSTRUM_TEST_KEEP", "base")
	t.Setenv("CLAUSTRUM_TEST_OVERRIDE", "old")

	env := buildEnv(map[string]string{
		"CLAUSTRUM_TEST_OVERRIDE": "new",
		"CLAUSTRUM_TEST_ADDED":    "added",
	})

	if !slices.Contains(env, "CLAUSTRUM_TEST_KEEP=base") {
		t.Error("inherited environ entry was dropped")
	}
	if !slices.Contains(env, "CLAUSTRUM_TEST_OVERRIDE=new") {
		t.Error("caller override was not applied")
	}
	if slices.Contains(env, "CLAUSTRUM_TEST_OVERRIDE=old") {
		t.Error("stale value survived the override")
	}
	if !slices.Contains(env, "CLAUSTRUM_TEST_ADDED=added") {
		t.Error("new caller key was not appended")
	}
}
