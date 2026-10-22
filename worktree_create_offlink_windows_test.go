//go:build windows

package main

import "testing"

// offLinkKinds on Windows: a junction (cell C2offJ) and a directory symbolic
// link (cell C2off) at the registrations folder. Each helper skips the test
// when mklink cannot make the link. The attach rows on Windows are not
// measured.
var offLinkKinds = []offLinkKind{
	{"junction", func(t *testing.T, at, target string) { makeJunction(t, at, target) }},
	{"dirsymlink", func(t *testing.T, at, target string) { makeDirSymlink(t, at, target) }},
}
