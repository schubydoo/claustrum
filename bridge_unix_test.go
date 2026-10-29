//go:build unix

package main

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestBridgeStdoutHidesReadFrom pins that the bridge writer offers no ReadFrom,
// so io.Copy makes plain writes.
func TestBridgeStdoutHidesReadFrom(t *testing.T) {
	if _, ok := bridgeStdout(os.Stdout).(io.ReaderFrom); ok {
		t.Fatal("bridgeStdout offers ReadFrom")
	}
}

// TestBridgeExitsZeroOnClosedStdout runs the real -bridge mode in a child. The
// test closes the read end of the child stdout after the first frame, then
// makes the daemon send another frame. The child must exit 0, not die by SIGPIPE.
func TestBridgeExitsZeroOnClosedStdout(t *testing.T) {
	_, sock := newRunningServer(t)
	exe, env := helperCommand(t, "bridge")
	env["CLAUSTRUM_TEST_BRIDGE_SOCK"] = sock
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Env = buildEnv(env)
	cmd.Stdin, cmd.Stdout = inR, outW
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = outW.Close()
	_ = inR.Close()
	ping := `{"jsonrpc":"2.0","id":1,"method":"server.ping","auth":"` + testToken + `"}` + "\n"
	if _, err := io.WriteString(inW, ping); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(outR).ReadString('\n')
	if err != nil || !strings.Contains(line, `"pong":true`) {
		t.Fatalf("first frame = %q, %v", line, err)
	}
	_ = outR.Close()
	if _, err := io.WriteString(inW, ping); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				t.Fatalf("bridge died by signal %v", ws.Signal())
			}
		}
		if err != nil {
			t.Fatalf("bridge exit = %v, want 0", err)
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("bridge did not exit")
	}
	_ = inW.Close()
}
