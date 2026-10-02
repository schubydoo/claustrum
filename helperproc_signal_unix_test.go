//go:build unix

package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

// execOnSigterm makes the helper child replace its program with path when SIGTERM
// comes (see the "term-exec" helper mode). The new program is the same test binary
// under another name, run as a 60 s sleeper.
func execOnSigterm(path string) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)
	go func() {
		<-ch
		env := replaceOrAppendEnv(os.Environ(), "CLAUSTRUM_TEST_HELPER", "sleep")
		_ = syscall.Exec(path, []string{path, "60"}, env)
		os.Exit(3)
	}()
}

// exitZeroOnSigtermAfter makes the helper child exit with code 0 after a SIGTERM, but
// only once the file gate exists (see the "term-exit0-gate" helper mode).
func exitZeroOnSigtermAfter(gate string) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)
	go func() {
		<-ch
		for {
			if _, err := os.Stat(gate); err == nil {
				os.Exit(0)
			}
			time.Sleep(time.Millisecond)
		}
	}()
}

// ignoreSigterm makes the helper child ignore SIGTERM, so process.killAndWait's
// graceful signal is a no-op and it must escalate to SIGKILL (see the
// "ignore-term" helper mode and TestKillAndWaitEscalation).
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

// exitZeroOnSigterm makes the helper child exit with code 0 when SIGTERM comes, the
// shape of a child that ends cleanly on a process.kill (see the "term-exit0" helper
// mode).
func exitZeroOnSigterm() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)
	go func() {
		<-ch
		os.Exit(0)
	}()
}
