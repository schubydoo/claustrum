//go:build linux || darwin

package sysproc

import (
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// exitCodeEnv makes the test binary exit at once with its value as the code.
// The value "hold" makes it sleep until it is killed.
const exitCodeEnv = "CLAUSTRUM_SYSPROC_EXIT_CODE"

// TestHelperExitWithCode is the child of TestAwaitExitStatus. It runs only with
// exitCodeEnv set.
func TestHelperExitWithCode(t *testing.T) {
	v := os.Getenv(exitCodeEnv)
	if v == "" {
		t.Skip("a helper of TestAwaitExitStatus")
	}
	if v == "hold" {
		time.Sleep(time.Minute)
	}
	code, _ := strconv.Atoi(v)
	os.Exit(code)
}

// TestAwaitExitStatus: the wait reports a clean exit only for status 0, not for
// exit 3 and not for a SIGKILL. It reaps nothing, so the wait of exec.Cmd
// still gets the status.
func TestAwaitExitStatus(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		env   string
		clean bool
		code  int
	}{
		{"exit 0", "0", true, 0},
		{"exit 3", "3", false, 3},
		{"SIGKILL", "hold", false, -1},
	} {
		cmd := exec.Command(exe, "-test.run=^TestHelperExitWithCode$")
		cmd.Env = append(os.Environ(), exitCodeEnv+"="+tc.env)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		if tc.env == "hold" {
			_ = cmd.Process.Kill()
		}
		seen, clean := AwaitExitStatus(cmd.Process.Pid)
		if !seen || clean != tc.clean {
			t.Errorf("%s: AwaitExitStatus = %v, %v, want true, %v", tc.name, seen, clean, tc.clean)
		}
		_ = cmd.Wait()
		if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != tc.code {
			t.Errorf("%s: the wait of exec.Cmd got %v, want exit code %d: the step reaped the child", tc.name, cmd.ProcessState, tc.code)
		}
	}
}
