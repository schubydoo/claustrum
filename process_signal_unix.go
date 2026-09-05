//go:build unix

package main

import (
	"errors"
	"os/exec"
	"syscall"
)

// exitSignalName returns the SIG-prefixed name of the signal that terminated the
// process, read from its wait status, or "" if it exited normally. The reference
// daemon emits this on the process exit stream frame since 4534d86 (measured:
// SIGTERM on a process.kill TERM, SIGKILL on a process.kill KILL). Windows has no
// signal on the wait path, so its build returns "" and the field is omitted.
func exitSignalName(waitErr error) string {
	var ee *exec.ExitError
	if !errors.As(waitErr, &ee) {
		return ""
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return ""
	}
	return signalName(ws.Signal())
}

// signalName maps a signal to its SIG-prefixed name. The map is keyed by the
// portable syscall.SIG* constants — their numeric values differ across unix
// variants but the constants do not, so it is correct on linux and darwin alike.
// It covers every signal a client kill can request (parseSignal collapses input
// to KILL/INT/HUP/TERM) plus the common crash signals a spawned process hits. An
// exotic externally-delivered signal (e.g. SIGXCPU) is unmapped and returns ""
// (the field is then omitted); the reference's output for those is unmeasured, so
// this is not widened on a guess.
func signalName(sig syscall.Signal) string {
	return signalNames[sig]
}

var signalNames = map[syscall.Signal]string{
	syscall.SIGHUP:  "SIGHUP",
	syscall.SIGINT:  "SIGINT",
	syscall.SIGQUIT: "SIGQUIT",
	syscall.SIGILL:  "SIGILL",
	syscall.SIGTRAP: "SIGTRAP",
	syscall.SIGABRT: "SIGABRT",
	syscall.SIGBUS:  "SIGBUS",
	syscall.SIGFPE:  "SIGFPE",
	syscall.SIGKILL: "SIGKILL",
	syscall.SIGUSR1: "SIGUSR1",
	syscall.SIGSEGV: "SIGSEGV",
	syscall.SIGUSR2: "SIGUSR2",
	syscall.SIGPIPE: "SIGPIPE",
	syscall.SIGALRM: "SIGALRM",
	syscall.SIGTERM: "SIGTERM",
}
