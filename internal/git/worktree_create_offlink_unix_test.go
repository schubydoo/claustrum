//go:build unix

package git

import "testing"

// offLinkKinds on Linux and macOS: a symbolic link (measured) and a file at
// the registrations folder.
var offLinkKinds = []offLinkKind{
	{"symlink", func(t *testing.T, at, target string) { linkAt(t, at, target) }},
	{"file", func(t *testing.T, at, _ string) { mustWrite(t, at, "x") }},
}
