//go:build unix

package main

import "testing"

// offLinkKinds on Linux and macOS: a symbolic link (cells C2off and C2offA)
// and a file (cell C2offF) at the registrations folder.
var offLinkKinds = []offLinkKind{
	{"symlink", func(t *testing.T, at, target string) { linkAt(t, at, target) }},
	{"file", func(t *testing.T, at, _ string) { mustWrite(t, at, "x") }},
}
