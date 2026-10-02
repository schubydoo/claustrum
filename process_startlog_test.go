package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSpawnChdirFailureIsLogged pins the Failed to start line of a spawn whose cwd is
// not usable: the text after the id is the message of the error frame. Measured
// against 89cb6289 on Linux (rows CW01 and CW02a) and on Windows (rows CW01 and
// CW02a). Before, claustrum returned these two errors with no log line.
func TestSpawnChdirFailureIsLogged(t *testing.T) {
	dir := t.TempDir()
	afile := filepath.Join(dir, "afile")
	if err := os.WriteFile(afile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	exe, env := helperCommand(t, "echo")
	for _, tc := range []struct {
		name, cwd, prefix string
	}{
		{"cwd does not exist", filepath.Join(dir, "nocwd"), "chdir " + filepath.Join(dir, "nocwd") + ": "},
		{"cwd is a regular file", afile, "chdir " + afile + ": not a directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogBuf(t)
			m := newTestProcManager(t)
			_, err := m.spawn(nil, "p1", exe, []string{"hi"}, tc.cwd, env, false)
			if err == nil || !strings.HasPrefix(err.Error(), tc.prefix) {
				t.Fatalf("spawn error = %v, want one that starts with %q", err, tc.prefix)
			}
			if want := "[process.Manager] Failed to start process p1: " + err.Error() + "\n"; !strings.Contains(logs.String(), want) {
				t.Errorf("log = %q\nwant it to hold %q", logs.String(), want)
			}
		})
	}
}

// TestExitLogSuffix pins the tail of the exited line for each pair of facts that the
// exit frame holds. The first three rows are measured against 89cb6289 on Linux (rows
// SP01, SP03a and SP04). The last row is the text of 89cb6289 in Linux row STb. The
// row with SIGTERM and a client request is not measured.
func TestExitLogSuffix(t *testing.T) {
	for _, tc := range []struct {
		signal, killedBy, want string
	}{
		{"", "", ""},
		{"", "client", ", signalled at client request"},
		{"SIGKILL", "", ", terminated by SIGKILL"},
		{"SIGTERM", "client", ", terminated by SIGTERM, signalled at client request"},
		{"SIGKILL", "shutdown", ", terminated by SIGKILL, signalled at shutdown request"},
	} {
		if got := exitLogSuffix(tc.signal, tc.killedBy); got != tc.want {
			t.Errorf("exitLogSuffix(%q, %q) = %q, want %q", tc.signal, tc.killedBy, got, tc.want)
		}
	}
}
