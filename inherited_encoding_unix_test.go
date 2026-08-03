//go:build unix

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// Two more inherited surfaces, stacked in one frame, and they are inherited on
// the INBOUND side — which the outbound tests in inherited_encoding_test.go do
// not cover.
//
//  1. encoding/json replaces invalid UTF-8 when it DECODES a request into a Go
//     string, not only when it encodes a reply. A client that names a file whose
//     bytes are not valid UTF-8 therefore cannot address it: the daemon receives
//     the U+FFFD-substituted name and operates on that. This is a real
//     reachability limit of the protocol, and it is the stdlib's rule, not
//     claustrum's.
//
//  2. Go's *PathError formats as `op + " " + path + ": " + errno`, nesting one
//     inside another when a chdir fails, and that text reaches the wire verbatim
//     in process.spawn's error message.
//
// THE REQUEST LINE IS BUILT BY HAND, and that is the whole point. An earlier
// version passed the path through rpcLine, which marshals params with
// json.Marshal — so the invalid byte was replaced by an escape sequence in the
// TEST's own request, before the daemon ever decoded anything. Both direction
// assertions below were then true by construction and would have passed against
// a decoder that substituted nothing. Writing the raw byte into the line is what
// makes the daemon's decoder the thing under test.
//
// The two hypotheses are genuinely distinguishable this way:
//
//	decoder substitutes     the Go string holds a valid U+FFFD, so the reply
//	                        carries that CHARACTER in raw UTF-8
//	decoder passes through  the Go string holds the invalid byte, so the ENCODER
//	                        substitutes instead and the reply carries the six-
//	                        character escape TEXT (backslash, u, f, f, f, d)
//
// Verified directly: json.Unmarshal of {"cwd":"no<0xff>such"} yields the bytes
// 6e 6f ef bf bd 73 75 63 68 — the decoder substitutes, and re-marshalling that
// emits the raw character rather than an escape. So the CHARACTER is the
// expected observation and the escape text is the failure signal.
//
// Measured against the reference at 5db5e4a, both daemons answered identically:
//
//	chdir <dir>/no<U+FFFD>such: stat <dir>/no<U+FFFD>such: no such file or directory
//
// unix-only: the fixture needs a path byte that is not valid UTF-8. Windows
// paths are UTF-16 and cannot hold one, so the case does not exist there.
//
// The command comes from the test binary rather than /bin/echo, per the repo's
// fixture rule. It is never executed — the chdir fails first — but naming a
// system binary would still be the portability trap the platform sweep looks for.
func TestInheritedPathErrorTextReachesTheWire(t *testing.T) {
	dir := t.TempDir()
	exe, _ := helperCommand(t, "echo")

	sock := startSocketServer(t)
	cl := dial(t, sock)

	// The raw 0xff byte goes into the line verbatim. testClient.send writes
	// []byte(line+"\n"), so nothing between here and the daemon's decoder
	// re-encodes it.
	missing := filepath.Join(dir, "no\xffsuch")
	line := `{"jsonrpc":"2.0","id":1,"method":"process.spawn","params":{"id":"p1","command":"` +
		jsonStringBody(exe) + `","cwd":"` + jsonStringBody(missing) + `"}}`
	got := string(cl.call(authed(line)))

	// Assert the SHAPE, not the absolute path: the tempdir differs per run.
	// "�" here is the CHARACTER — see the header for why that is the tell.
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

	// Pin the DIRECTION. Either of these would mean the substitution moved sides,
	// and neither can be satisfied trivially now that the byte is sent raw.
	if strings.Contains(got, "no\xffsuch") {
		t.Error("the invalid path byte survived decoding; encoding/json's decode-side " +
			"replacement is what makes such a filename unreachable, on both daemons")
	}
	// escapeText is the six ASCII characters of a \u escape, assembled from parts.
	// Written as one literal it is liable to be collapsed into the character it
	// denotes by an editing pass — which already happened here once and inverted
	// this very check, since the reply legitimately DOES contain the character.
	escapeText := "\\" + "ufffd"
	if strings.Contains(got, escapeText) {
		t.Errorf("the message carries the %s ESCAPE, so the string still held the "+
			"invalid byte at marshal time and the ENCODER substituted — the reference "+
			"substitutes at decode", escapeText)
	}
}

// jsonStringBody escapes s for use inside a JSON string literal, WITHOUT
// touching bytes that are merely invalid UTF-8 — which json.Marshal would
// replace, defeating the test above. Only the two characters that would break
// the literal are escaped.
func jsonStringBody(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}
