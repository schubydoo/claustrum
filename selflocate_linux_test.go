package main

import (
	"io"
	"os"
	"path/filepath"
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
