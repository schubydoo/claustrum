//go:build unix

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// Two more inherited surfaces, stacked in one frame — and they are inherited on
// the INBOUND side, which the outbound tests in inherited_encoding_test.go do
// not cover.
//
//  1. encoding/json replaces invalid UTF-8 when it DECODES a request into a Go
//     string, not only when it encodes one. A client that names a file whose
//     bytes are not valid UTF-8 therefore cannot address it: the daemon receives
//     the U+FFFD-substituted name and operates on that. This is a real
//     reachability limit of the protocol, and it is the stdlib's rule, not
//     claustrum's.
//
//  2. Go's *PathError formats as `op + " " + path + ": " + errno`, nesting one
//     inside another when a chdir fails, and that text reaches the wire verbatim
//     in process.spawn's error message.
//
// Measured against the reference at 5db5e4a, both daemons answered identically:
//
//	chdir <dir>/no<U+FFFD>such: stat <dir>/no<U+FFFD>such: no such file or directory
//
// Note what the message carries: the U+FFFD CHARACTER in raw UTF-8, not a
// "�" escape sequence. That is the tell for which side substituted. By the
// time the string is marshalled it is valid UTF-8, so the encoder passes it
// through untouched — the replacement already happened at decode. The outbound
// tests assert escape TEXT for exactly the opposite reason.
//
// unix-only: the fixture needs a path byte that is not valid UTF-8. Windows
// paths are UTF-16 and cannot hold one, so the case does not exist there.
//
// The command comes from the test binary rather than /bin/echo, per the repo's
// fixture rule. It is never executed — the chdir fails first — but naming a
// system binary would still be the portability trap the platform sweep looks for.
func TestInheritedPathErrorTextReachesTheWire(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "no\xffsuch")
	exe, env := helperCommand(t, "echo")

	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "process.spawn", map[string]any{
		"id": "p1", "command": exe, "args": []string{}, "cwd": missing, "env": env,
	}))

	// Assert the SHAPE, not the absolute path: the tempdir differs per run.
	// "�" here is the CHARACTER, deliberately — see the header.
	for _, want := range []string{
		"chdir ", ": stat ", ": no such file or directory", "no�such",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("process.spawn chdir failure\n got: %s\nmissing substring: %q\n\n"+
				"this string is Go's *PathError formatting plus encoding/json's "+
				"decode-side U+FFFD substitution; both are inherited, and both are "+
				"on the wire.", got, want)
		}
	}

	// And pin the direction: the raw byte must NOT survive, and the escape TEXT
	// must not appear either. Either would mean the substitution moved sides.
	if strings.Contains(got, "no\xffsuch") {
		t.Error("the invalid path byte survived decoding; encoding/json's decode-side " +
			"replacement is what makes such a filename unreachable, on both daemons")
	}
	if strings.Contains(got, "\\ufffd") {
		t.Error("the message carries the \\ufffd ESCAPE, so the substitution happened " +
			"at encode rather than decode — the reference substitutes at decode")
	}
}
