package git

import "path/filepath"

// The functions below let the tests of package main reach state of this
// package. Only tests call them.

// ShadowFolderForTest returns the folder of all daemons below the run folder
// runDir: <runDir>/git-shadow.
func ShadowFolderForTest(runDir string) string {
	return filepath.Join(runDir, shadowRootName)
}

// ResetUserExcludesCacheForTest makes the next userExcludesFile call resolve
// again. It returns a func that puts the old value back. Each change counts in
// userExcludesGen, so a read that began before it stores nothing.
func ResetUserExcludesCacheForTest() (restore func()) {
	userExcludesMu.Lock()
	prevDone, prevValue := userExcludesDone, userExcludesCached
	userExcludesDone, userExcludesCached = false, ""
	userExcludesGen++
	userExcludesMu.Unlock()
	return func() {
		userExcludesMu.Lock()
		userExcludesDone, userExcludesCached = prevDone, prevValue
		userExcludesGen++
		userExcludesMu.Unlock()
	}
}
