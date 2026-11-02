//go:build !windows

package install

import (
	"runtime"
	"testing"
)

// TestManagedIgnoredVarLineBytes pins the measured text of the RXs log line, with
// the system folder of this OS (Linux and macOS texts both measured).
func TestManagedIgnoredVarLineBytes(t *testing.T) {
	want := map[string]string{
		"linux":  "[launcher] CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR ignored: /etc/claude-code holds this host's policy",
		"darwin": "[launcher] CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR ignored: /Library/Application Support/ClaudeCode holds this host's policy",
	}[runtime.GOOS]
	if want == "" {
		t.Skip("no measured text for " + runtime.GOOS)
	}
	if got := managedIgnoredVarLine(); got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
}
