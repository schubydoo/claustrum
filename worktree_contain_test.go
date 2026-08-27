package main

import (
	"path/filepath"
	"testing"
)

// filepath.Rel cannot relate a relative path to an absolute root; that error must
// read as "not inside" rather than panic or accept. The equal-path case is "not
// inside" by definition (the repo root stays out of reach of the remove fallback).
func TestPathStrictlyUnderRelError(t *testing.T) {
	root := t.TempDir()
	if pathStrictlyUnder(filepath.Join("rel", "x"), root) {
		t.Error("a relative path must not be reported as strictly under an absolute root")
	}
	if pathStrictlyUnder(root, root) {
		t.Error("the root itself must not be strictly under itself")
	}
}
