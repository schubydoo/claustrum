package main

import "testing"

// resetUserExcludesCache forces the next userExcludesFile call to resolve again.
//
// userExcludesFile memoizes for the life of the process, which is right in
// production: the daemon resolves the user's global excludes once at the first git
// op and every later op agrees with it. In a test it means the FIRST test to run a
// git command fixes the value for the whole binary, so a later test that moves
// HOME or XDG_CONFIG_HOME gets no effect at all.
//
// The write goes through the same mutex the reader takes, so it cannot race an
// in-flight request goroutine that is running a git op.
//
// A test that cares about ignore state calls isolateGitConfig, which calls this.
func resetUserExcludesCache(t *testing.T) {
	t.Helper()
	userExcludesMu.Lock()
	prevDone, prevValue := userExcludesDone, userExcludesCached
	userExcludesDone, userExcludesCached = false, ""
	userExcludesMu.Unlock()
	t.Cleanup(func() {
		userExcludesMu.Lock()
		userExcludesDone, userExcludesCached = prevDone, prevValue
		userExcludesMu.Unlock()
	})
}
