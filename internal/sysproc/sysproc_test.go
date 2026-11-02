package sysproc

import (
	"bytes"
	"slices"
	"testing"
)

// Kept is the kept head alone: the dropped-byte count of String is not in it.
// gitrepoconfig.go of package main returns these bytes as the error output of git.
func TestCappedTextKeptHasNoDroppedCount(t *testing.T) {
	const limit = 1 << 16
	c := &CappedText{Max: limit}
	in := bytes.Repeat([]byte("0123456789abcdef"), limit/16+10)
	_, _ = c.Write(in)
	if got := c.Kept(); !bytes.Equal(got, in[:limit]) {
		t.Errorf("Kept: len %d, want the first %d bytes with no dropped-byte count", len(got), limit)
	}
}

func TestReplaceOrAppendEnv(t *testing.T) {
	// Existing key is replaced in place.
	got := ReplaceOrAppendEnv([]string{"A=1", "B=2"}, "A", "9")
	if want := []string{"A=9", "B=2"}; !slices.Equal(got, want) {
		t.Errorf("replace: got %v, want %v", got, want)
	}

	// New key is appended.
	got = ReplaceOrAppendEnv([]string{"A=1"}, "C", "3")
	if want := []string{"A=1", "C=3"}; !slices.Equal(got, want) {
		t.Errorf("append: got %v, want %v", got, want)
	}

	// Prefix match must be exact ("A=" not a prefix of "AB=").
	got = ReplaceOrAppendEnv([]string{"AB=2"}, "A", "1")
	if want := []string{"AB=2", "A=1"}; !slices.Equal(got, want) {
		t.Errorf("prefix safety: got %v, want %v", got, want)
	}
}
