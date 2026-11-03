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
	userExcludesMu.Lock()
	genBefore := userExcludesGen
	userExcludesMu.Unlock()
	restore := ResetUserExcludesCacheForTest()
	userExcludesMu.Lock()
	doneDuring, valueDuring, genDuring := userExcludesDone, userExcludesCached, userExcludesGen
	userExcludesMu.Unlock()
	restore()
	userExcludesMu.Lock()
	doneAfter, valueAfter, genAfter := userExcludesDone, userExcludesCached, userExcludesGen
	userExcludesMu.Unlock()
	// Each change counts, so a read that began before it stores nothing.
	if genDuring != genBefore+1 || genAfter != genBefore+2 {
		t.Errorf("generation = %d during, %d after, from %d; want +1, then +2", genDuring, genAfter, genBefore)
	}
	if doneDuring || valueDuring != "" || !doneAfter || valueAfter != "/cached/ignore" {
		t.Errorf("cache = (%v, %q) during, (%v, %q) after; want empty, then the old value",
			doneDuring, valueDuring, doneAfter, valueAfter)
	}
}
