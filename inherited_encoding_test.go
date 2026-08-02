package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Wire bytes claustrum emits because it is written in Go, not because any
// claustrum code chose them.
//
// The reference is also Go, so its stdlib is doing unpaid parity work for us:
// encoding/json's defaults, os's error formatting and path/filepath's cleaning
// all reach the wire directly. That is load-bearing and it was, until this file,
// entirely untested — the validation battery only exercises the spellings its
// fixtures send, and none of them sent any of these shapes.
//
// Every case here was measured against the reference at 5db5e4a and agreed byte
// for byte. What these tests protect is the OTHER direction: a Go upgrade that
// changes an escaping rule, or a refactor that disables HTML escaping, would move
// the wire silently. Here it fails instead.
//
// THEY GO THROUGH THE SOCKET, deliberately. An earlier version drove dispatch
// directly via dispatchRaw, which marshals the response with its OWN
// json.Marshal call — so it asserted encoding/json's behaviour but never touched
// conn.writeResponse, and the exact refactor server.go warns about (swapping
// json.Marshal for an Encoder with SetEscapeHTML(false)) would have kept these
// green. Running over a real connection makes the asserted bytes the ones a
// client actually receives.
//
// The expected strings are written with doubled backslashes on purpose — they
// assert the six ASCII characters of a JSON escape sequence, not the character
// it denotes. Writing the character itself would assert the opposite of the
// rule under test.
//
// See docs/ARCHITECTURE.md → "Inherited wire bytes" for the full register.

// readContentFrame writes blob to a file, reads it back through a real socket
// round trip, and returns the raw reply bytes.
func readContentFrame(t *testing.T, blob []byte) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, blob, 0o600); err != nil {
		t.Fatal(err)
	}
	sock := startSocketServer(t)
	cl := dial(t, sock)
	// filepath.ToSlash keeps the JSON literal free of Windows backslashes, which
	// would otherwise need escaping in this hand-built line.
	line := `{"jsonrpc":"2.0","id":1,"method":"files.read","params":{"path":"` +
		filepath.ToSlash(p) + `"}}`
	return string(cl.call(authed(line)))
}

// encoding/json replaces bytes that are not valid UTF-8 with U+FFFD when it
// marshals a Go string — one replacement per invalid byte — and escapes NUL
// numerically. files.read puts raw file bytes straight into a string field, so
// this rule is on the wire for every non-text file a client reads.
func TestInheritedInvalidUTF8BecomesReplacementChar(t *testing.T) {
	got := readContentFrame(t, []byte("head\xff\xfe\x00tail\n"))
	want := "\"content\":\"head\\ufffd\\ufffd\\u0000tail\\n\""
	if !strings.Contains(got, want) {
		t.Errorf("files.read of invalid UTF-8\n got: %s\nwant substring: %s\n\n"+
			"encoding/json's invalid-UTF-8 handling reaches the wire here; if it "+
			"changed, every non-text files.read diverges from the reference.", got, want)
	}
}

// A lone surrogate encoded as UTF-8 (ED A0 80) is invalid UTF-8, and Go replaces
// all three bytes rather than emitting a \ud800 escape. Called out separately
// because a hand-rolled encoder is most likely to differ exactly here.
func TestInheritedLoneSurrogateBecomesThreeReplacements(t *testing.T) {
	got := readContentFrame(t, []byte("pre\xed\xa0\x80post\n"))
	want := "\"content\":\"pre\\ufffd\\ufffd\\ufffdpost\\n\""
	if !strings.Contains(got, want) {
		t.Errorf("files.read of a lone surrogate\n got: %s\nwant substring: %s", got, want)
	}
}

// encoding/json HTML-escapes <, > and & by default, and json.Marshal offers no
// way to opt out (only an Encoder does, via SetEscapeHTML(false)). Any file
// holding HTML or shell redirection comes back escaped.
//
// This is the assertion that guards conn.writeResponse specifically: it is the
// function the "switch to an Encoder" refactor would change, and the bytes
// checked here came out of it.
//
// Do NOT "fix" the expectation into literal characters: that is a wire change,
// not a cleanup.
func TestInheritedHTMLEscapingReachesContent(t *testing.T) {
	got := readContentFrame(t, []byte("a<b>c&d\n"))
	want := "\"content\":\"a\\u003cb\\u003ec\\u0026d\\n\""
	if !strings.Contains(got, want) {
		t.Errorf("files.read of HTML-escapable bytes\n got: %s\nwant substring: %s\n\n"+
			"if this shows literal angle brackets, something disabled encoding/json's "+
			"HTML escaping and every such frame now differs from the reference.", got, want)
	}
}

// The decode-side surface — Go's *PathError text plus the request decoder's own
// U+FFFD substitution — lives in inherited_encoding_unix_test.go. It needs a
// path holding a byte that is not valid UTF-8, which is a POSIX notion: Windows
// paths are UTF-16 and cannot carry one, so the case does not exist there.
