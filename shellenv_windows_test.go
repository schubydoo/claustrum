//go:build windows

package main

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"
)

// setDaemonPathSpelledPath gives the test process one path entry, named exactly Path,
// with the value val. The cleanup puts back the entry of the runner with its own name.
// A runner can name the entry PATH, so a plain Setenv("Path") can keep that name.
// These tests change the process environment, so they stay sequential (no t.Parallel).
func setDaemonPathSpelledPath(t *testing.T, val string) {
	t.Helper()
	var name, old string
	for _, e := range os.Environ() {
		if k, v, ok := strings.Cut(e, "="); ok && strings.EqualFold(k, "PATH") {
			name, old = k, v
		}
	}
	t.Cleanup(func() {
		_ = os.Unsetenv("PATH")
		if name != "" {
			_ = os.Setenv(name, old)
		}
	})
	_ = os.Unsetenv("PATH")
	_ = os.Setenv("Path", val)
	found := false
	for _, e := range os.Environ() {
		if e == "Path="+val {
			found = true
		}
	}
	if !found {
		t.Fatal("the test process holds no entry named Path after the set")
	}
}

// childPathEntries spawns the environ helper through process.spawn and returns
// each entry of the child whose name is PATH in any case.
func childPathEntries(t *testing.T, id string, caller map[string]string) []string {
	t.Helper()
	m := newTestProcManager(t)
	t.Cleanup(m.killAll)
	c, frames := pipeConn(t)
	exe, env := helperCommand(t, "environ")
	for k, v := range caller {
		env[k] = v
	}
	if _, err := m.spawn(c, id, exe, nil, "", env, false); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	var out strings.Builder
	deadline := time.After(10 * time.Second)
	for done := false; !done; {
		select {
		case f := <-frames:
			switch f.Stream {
			case "stdout":
				b, err := base64.StdEncoding.DecodeString(f.Data)
				if err != nil {
					t.Fatalf("bad base64 in stdout frame: %v", err)
				}
				out.WriteString(string(b))
			case "exit":
				done = true
			}
		case <-deadline:
			t.Fatal("no exit frame within deadline")
		}
	}
	var got []string
	for _, e := range strings.Split(out.String(), "\n") {
		if k, _, ok := strings.Cut(e, "="); ok && strings.EqualFold(k, "PATH") {
			got = append(got, e)
		}
	}
	return got
}

// TestSpawnChildGetsTheDaemonPATHOnWindows holds the Windows rule of the child path
// entry, measured on a Windows VM against 89cb6289. The daemon holds Path=SYS. The
// child gets PATH=SYS and no Path entry (R1). A caller Path value is lost there (R4). A
// caller PATH value wins (R3). The read runs through the real extractor.
func TestSpawnChildGetsTheDaemonPATHOnWindows(t *testing.T) {
	sys := `C:\claustrum-daemon-path;` + os.Getenv("PATH")
	const caller = `C:\x;C:\Windows\system32`
	cases := []struct {
		row    string
		caller map[string]string
		want   string
	}{
		{"R1", nil, "PATH=" + sys},
		{"R4", map[string]string{"Path": caller}, "PATH=" + sys},
		{"R3", map[string]string{"PATH": caller}, "PATH=" + caller},
	}
	for _, tc := range cases {
		t.Run(tc.row, func(t *testing.T) {
			origExtractor := loginPATHExtractor
			t.Cleanup(func() {
				loginPATHExtractor = origExtractor
				resetLoginPATHForTest()
			})
			resetLoginPATHForTest()
			loginPATHExtractor = extractLoginPATH
			setDaemonPathSpelledPath(t, sys)
			armLoginPATH()

			got := childPathEntries(t, "path-"+tc.row, tc.caller)
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("row %s: the child path entries are %q, want only %q", tc.row, got, tc.want)
			}
		})
	}
}

// TestNoDaemonPATHGivesNoChildPATHOnWindows holds row R6: a daemon with no path
// entry gives the child no path entry. Its result does not depend on the
// Windows read, and it passes with or without the fix.
func TestNoDaemonPATHGivesNoChildPATHOnWindows(t *testing.T) {
	origExtractor := loginPATHExtractor
	t.Cleanup(func() {
		loginPATHExtractor = origExtractor
		resetLoginPATHForTest()
	})
	resetLoginPATHForTest()
	loginPATHExtractor = extractLoginPATH
	setDaemonPathSpelledPath(t, "x")
	_ = os.Unsetenv("PATH")
	armLoginPATH()

	for _, e := range buildEnv(nil) {
		if k, _, ok := strings.Cut(e, "="); ok && strings.EqualFold(k, "PATH") {
			t.Errorf("the child environment holds %q, want no path entry", e)
		}
	}
}
