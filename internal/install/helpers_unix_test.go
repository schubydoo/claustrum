//go:build !windows

package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
)

// The helpers of package main that the Linux and macOS tests of this package
// use, under the same names.

// ignoreSigterm makes the helper child ignore SIGTERM.
func ignoreSigterm() { signal.Ignore(syscall.SIGTERM) }

// stubProcIDs is the process group, the session and the parent of this helper
// process, for the START line of the "cli-stub" mode.
func stubProcIDs() string {
	return fmt.Sprintf("pgid=%d sid=%d ppid=%d", syscall.Getpgrp(), ownSessionID(), os.Getppid())
}

// ownSessionID is getsid(0). The syscall package has no Getsid on Linux.
func ownSessionID() int {
	sid, _, _ := syscall.RawSyscall(syscall.SYS_GETSID, 0, 0, 0)
	return int(sid)
}

// stubNewSession starts a child of the "cli-stub" mode in a new session.
func stubNewSession(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

// tempManagedSettingsDir points the managed-settings folder at a fresh temp folder and returns it.
// It also points the system folder at an absent path. managedSettingsDir reads the
// system folder first, and a policy on the host there makes the resolve ignore the
// variable.
func tempManagedSettingsDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	setManagedSystemDir(t, filepath.Join(t.TempDir(), "absent-system"))
	t.Setenv(managedSettingsDirEnv, d)
	return d
}

// noManagedPolicy makes sure that no managed policy applies for one test: the
// user settings folder is an empty fixture folder and the system folder an absent
// path (tempManagedSettingsDir).
func noManagedPolicy(t *testing.T) {
	t.Helper()
	_ = tempManagedSettingsDir(t)
}

// setManagedSystemDir points the system managed-settings folder at dir for one test.
func setManagedSystemDir(t *testing.T, dir string) {
	t.Helper()
	old := managedSystemSettingsDir
	managedSystemSettingsDir = dir
	t.Cleanup(func() { managedSystemSettingsDir = old })
}

// writeFileMode writes b to p with exactly mode, creating the parent folders.
func writeFileMode(t *testing.T, p string, b []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil { // WriteFile's mode passes through the umask
		t.Fatal(err)
	}
}

// jsonText is v as a JSON string literal, for building a settings file.
func jsonText(t *testing.T, v string) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func pwSettings(t *testing.T, v string) string {
	return `{"processWrapper":` + jsonText(t, v) + `}`
}

// syncBuffer is the locked sink of captureLogBuf. The test goroutine reads it
// while other goroutines can still write to the logger.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogBuf points the default logger at a locked buffer, with no timestamp,
// until the test ends, and returns the buffer.
func captureLogBuf(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldW); log.SetFlags(oldF) })
	return buf
}

// useLddStub writes script as an executable stub and points the ldd probe at it.
func useLddStub(t *testing.T, dir, script string) {
	t.Helper()
	stub := filepath.Join(dir, "ldd")
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := lddPath
	t.Cleanup(func() { lddPath = old })
	lddPath = stub
}
