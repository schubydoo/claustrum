package main

import "testing"

// exitPanic is the sentinel the osExit test stub panics with; catchExit
// translates it back into an exit code.
type exitPanic struct{ code int }

// stubOsExit swaps osExit for a panicking stand-in for the rest of the test,
// letting the daemon-lifecycle shells (runServe, daemonizeWithToken, teardown,
// main) run in-process. Panicking — rather than returning — preserves
// os.Exit's no-return contract at every call site.
func stubOsExit(t *testing.T) {
	t.Helper()
	old := osExit
	osExit = func(code int) { panic(exitPanic{code}) }
	t.Cleanup(func() { osExit = old })
}

// catchExit runs f under the stub, reporting a stubbed exit as (code, true).
// (0, false) means f returned without exiting. Any other panic re-panics.
func catchExit(f func()) (code int, exited bool) {
	defer func() {
		if r := recover(); r != nil {
			ep, ok := r.(exitPanic)
			if !ok {
				panic(r)
			}
			code, exited = ep.code, true
		}
	}()
	f()
	return 0, false
}
