//go:build linux

package main

import (
	"strings"
	"testing"
)

// TestParseStartTicksMalformed covers the two refusals of the /proc/<pid>/stat start-time
// parser. Its result is half of CLAUDE_SSH_CHILD, the pid-reuse-proof identity a later
// daemon matches a recorded child against, so a wrong number is worse than a zero.
//
// Each fixture is built so a DELETED guard answers differently: the no-')' line parses
// cleanly from index 0 (the mutant returns 9000 for a line the kernel never writes), and
// the short line makes the mutant index past the end.
func TestParseStartTicksMalformed(t *testing.T) {
	fields16 := strings.Repeat("0 ", 16)

	if got := parseStartTicks("R 1 2 " + fields16 + "9000 x"); got != 0 {
		t.Errorf("parseStartTicks on a line with no ')' = %d, want 0", got)
	}
	if got := parseStartTicks("42 (server) R 1 42 too short"); got != 0 {
		t.Errorf("parseStartTicks on a short line = %d, want 0", got)
	}
	// A well-formed line still parses, so the two zeros above are refusals and not a
	// parser that always answers zero.
	if got := parseStartTicks("42 (server) R 1 42 " + fields16 + "12345 x"); got != 12345 {
		t.Errorf("parseStartTicks on a valid line = %d, want 12345", got)
	}
	// The comm field may itself contain a ')', which is why parsing resumes after the LAST
	// one rather than the first.
	if got := parseStartTicks("42 (we ird) x) R 1 42 " + fields16 + "777 x"); got != 777 {
		t.Errorf("parseStartTicks with a ')' inside comm = %d, want 777", got)
	}
}
