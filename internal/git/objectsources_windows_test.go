//go:build windows

package git

import (
	"strings"
	"testing"
)

// The link chain text names a path sent with forward slashes in backslashes
// (measured on Windows).
func TestLinkChainTextBackslashes(t *testing.T) {
	if got := linkChainText("C:/work/L32"); !strings.HasPrefix(got, "Claude couldn't check that `C:\\work\\L32` stays on a local drive") {
		t.Errorf("got %q", got)
	}
}
