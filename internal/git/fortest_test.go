package git

import (
	"path/filepath"
	"testing"
)

// TestShadowFolderForTestNamesGitShadow: the hook names the folder that the
// host makes below its run folder.
func TestShadowFolderForTestNamesGitShadow(t *testing.T) {
	run := t.TempDir()
	if got, want := ShadowFolderForTest(run), filepath.Join(run, "git-shadow"); got != want {
		t.Errorf("ShadowFolderForTest(%q) = %q, want %q", run, got, want)
	}
}

// TestResetUserExcludesCacheForTestRestores: the hook empties the cache, and
// the returned func puts the old value back.
func TestResetUserExcludesCacheForTestRestores(t *testing.T) {
	userExcludesMu.Lock()
	oldDone, oldValue := userExcludesDone, userExcludesCached
	userExcludesDone, userExcludesCached = true, "/cached/ignore"
	userExcludesMu.Unlock()
	t.Cleanup(func() {
		userExcludesMu.Lock()
		userExcludesDone, userExcludesCached = oldDone, oldValue
		userExcludesMu.Unlock()
	})
	restore := ResetUserExcludesCacheForTest()
	userExcludesMu.Lock()
	doneDuring, valueDuring := userExcludesDone, userExcludesCached
	userExcludesMu.Unlock()
	restore()
	userExcludesMu.Lock()
	doneAfter, valueAfter := userExcludesDone, userExcludesCached
	userExcludesMu.Unlock()
	if doneDuring || valueDuring != "" || !doneAfter || valueAfter != "/cached/ignore" {
		t.Errorf("cache = (%v, %q) during, (%v, %q) after; want empty, then the old value",
			doneDuring, valueDuring, doneAfter, valueAfter)
	}
}
