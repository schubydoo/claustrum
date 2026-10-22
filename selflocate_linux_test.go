package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestServeLauncherStopsWithoutSelfExe pins the rule of round tier3-02
// (Linux VM). With no readable /proc/self/exe the -serve launcher prints one
// line, exits 1 and makes nothing in the socket folder. The token file stays.
// The os.Executable error is the one that Go returns on Linux for that state.
func TestServeLauncherStopsWithoutSelfExe(t *testing.T) {
	for _, tc := range []struct {
		name    string
		missing bool
	}{
		{"existing socket folder", false},
		{"missing socket folder", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubOsExit(t)
			// Without the fix the launcher re-execs the test binary. This seam makes
			// that child an exit-0 helper and the wait short, so a regression fails fast.
			shortDaemonStart(t)
			t.Setenv("CLAUSTRUM_TEST_HELPER", "exit:0")
			t.Setenv(daemonChildEnv, "")
			stubOsExecutable(t, "", &os.PathError{Op: "readlink", Path: "/proc/self/exe", Err: syscall.ENOENT})

			base := shortTempDir(t)
			tf := filepath.Join(base, "token")
			if err := os.WriteFile(tf, []byte("tok\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			dir := base
			if tc.missing {
				dir = filepath.Join(base, "run")
			}
			sock := filepath.Join(dir, "rpc.sock")

			old := os.Stderr
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			os.Stderr = w
			code, exited := catchExit(func() { runServe(sock, tf, -1, "", wireLogOptions{}, false, false) })
			_ = w.Close()
			os.Stderr = old

			if !exited || code != 1 {
				t.Errorf("runServe: exited=%v code=%d, want exit 1", exited, code)
			}
			out, _ := io.ReadAll(r)
			if want := "claustrum: locate self: readlink /proc/self/exe: no such file or directory\n"; string(out) != want {
				t.Errorf("stderr = %q, want %q", out, want)
			}
			entries, err := os.ReadDir(base)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != "token" {
				var names []string
				for _, e := range entries {
					names = append(names, e.Name())
				}
				t.Errorf("entries of %s = %v, want only the token file", base, names)
			}
		})
	}
}

// TestServeLauncherFallsBackToArgsZero pins the arm of macOS and Windows on
// Linux. With selfLocateFails off, a failed os.Executable makes the launcher
// re-exec os.Args[0] and go on to the start of the child.
func TestServeLauncherFallsBackToArgsZero(t *testing.T) {
	oldFails := selfLocateFails
	selfLocateFails = false
	t.Cleanup(func() { selfLocateFails = oldFails })
	stubOsExit(t)
	stubOsExecutable(t, "", errNoExecutable)
	var started []string
	oldStart := startDaemonChild
	startDaemonChild = func(cmd *exec.Cmd) error { started = cmd.Args; return errStartRefused }
	t.Cleanup(func() { startDaemonChild = oldStart })

	sock := filepath.Join(shortTempDir(t), "run", "rpc.sock")
	var code int
	var exited bool
	stderr := captureStderr(t, func() {
		code, exited = catchExit(func() { daemonizeWithToken(sock, "") })
	})

	if !exited || code != 1 {
		t.Errorf("daemonizeWithToken: exited=%v code=%d, want exit 1 from the stubbed start", exited, code)
	}
	if len(started) == 0 || started[0] != os.Args[0] {
		t.Errorf("the child starts as %q, want os.Args[0] %q", started, os.Args[0])
	}
	if want := "daemonize: " + errStartRefused.Error(); !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr, want)
	}
	if strings.Contains(stderr, "locate self") {
		t.Errorf("stderr = %q, want no locate self line", stderr)
	}
	if _, err := os.Stat(filepath.Dir(sock)); err != nil {
		t.Errorf("the socket folder: %v, want the launcher to make it", err)
	}
}
