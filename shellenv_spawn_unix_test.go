//go:build unix

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/schubydoo/claustrum/internal/shellenv"
)

// fakeLoginShell installs an executable script as $SHELL. The probe runs it as
// `<shell> -l -i -c <script>`, so "$4" is the probe's own script.
func fakeLoginShell(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fakeshell")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", p)
	return p
}

// TestSuiteStubsShellAgentSocket guards TestMain's stub: without it, any test
// that spawns a fixture runs the user's real login shell.
func TestSuiteStubsShellAgentSocket(t *testing.T) {
	ran := filepath.Join(t.TempDir(), "ran")
	fakeLoginShell(t, `touch '`+ran+`'; SSH_AUTH_SOCK=/tmp/real.sock; export SSH_AUTH_SOCK; exec /bin/sh -c "$4"`)
	if got := shellenv.AgentSocket(); got != "" {
		t.Errorf("shellenv.AgentSocket() = %q, want the suite stub's empty answer", got)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Error("a login shell ran: TestMain no longer stubs the lookup")
	}
}

// stubLoginPATHExtractor makes the read a stub that waits for its stop, for a test
// that boots runServe. The cleanup ends a read that the start left running.
func stubLoginPATHExtractor(t *testing.T) {
	t.Helper()
	old := shellenv.LoginPATHExtractor
	shellenv.LoginPATHExtractor = func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
	t.Cleanup(func() {
		resetLoginPATHForTest()
		shellenv.LoginPATHExtractor = old
	})
}
