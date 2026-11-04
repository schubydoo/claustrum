//go:build windows

package main

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/shellenv"
)

// realLoginPATHExtractor is the extractor of production. The package initializes
// it before TestMain puts the stub of the suite in its place.
var realLoginPATHExtractor = shellenv.LoginPATHExtractor

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
// entry, measured on Windows against 89cb6289. The daemon holds Path=SYS. The child
// gets PATH=SYS and no Path entry. A caller Path value is lost there. A
// caller PATH value wins. The read runs through the real extractor.
func TestSpawnChildGetsTheDaemonPATHOnWindows(t *testing.T) {
	sys := `C:\claustrum-daemon-path;` + os.Getenv("PATH")
	const caller = `C:\x;C:\Windows\system32`
	cases := []struct {
		name   string
		caller map[string]string
		want   string
	}{
		{"no PATH of the caller", nil, "PATH=" + sys},
		{"a Path of the caller", map[string]string{"Path": caller}, "PATH=" + sys},
		{"a PATH of the caller", map[string]string{"PATH": caller}, "PATH=" + caller},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origExtractor := shellenv.LoginPATHExtractor
			t.Cleanup(func() {
				shellenv.LoginPATHExtractor = origExtractor
				resetLoginPATHForTest()
			})
			resetLoginPATHForTest()
			shellenv.LoginPATHExtractor = realLoginPATHExtractor
			setDaemonPathSpelledPath(t, sys)
			armLoginPATH()

			got := childPathEntries(t, "path-"+strings.ReplaceAll(tc.name, " ", "-"), tc.caller)
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("%s: the child path entries are %q, want only %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestNoDaemonPATHGivesNoChildPATHOnWindows holds a measured rule: a daemon with no path
// entry gives the child no path entry. Its result does not depend on the Windows read,
// and it passes with or without the fix.
func TestNoDaemonPATHGivesNoChildPATHOnWindows(t *testing.T) {
	origExtractor := shellenv.LoginPATHExtractor
	t.Cleanup(func() {
		shellenv.LoginPATHExtractor = origExtractor
		resetLoginPATHForTest()
	})
	resetLoginPATHForTest()
	shellenv.LoginPATHExtractor = realLoginPATHExtractor
	setDaemonPathSpelledPath(t, "x")
	_ = os.Unsetenv("PATH")
	armLoginPATH()

	for _, e := range buildEnv(nil) {
		if k, _, ok := strings.Cut(e, "="); ok && strings.EqualFold(k, "PATH") {
			t.Errorf("the child environment holds %q, want no path entry", e)
		}
	}
}
