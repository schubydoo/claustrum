//go:build !windows

package git

import "testing"

// TestStoreFullStart: a download line with a write below the private LFS store
// that ends in a full disk or a quota gives its own start and code, in any
// letter case. A path outside the store, a quoted path and an empty path give
// none (4c111e86, measured on Linux and macOS). The bounds of the store itself,
// of a missing store and of the line are claustrum's own. Windows has no private
// LFS store, so storeFullStart is not reached there and the test does not run.
func TestStoreFullStart(t *testing.T) {
	const run = "/h/.claude/remote/run/c1"
	st := run + "/git-shadow/s/.checkout-1/lfs"
	store := &filterRule{shadow: true, store: &storeUse{runDir: run, path: st}}
	noSpace := "The disk that holds " + run + " on the remote machine filled up during the checkout: Claude keeps this checkout's Git LFS downloads there while it runs." +
		" Free some space on that disk, or turn off Worktree for this session, then try again. "
	quota := "Your disk quota ran out during the checkout on the disk that holds " + run + " on the remote machine: Claude keeps this checkout's Git LFS downloads there while it runs." +
		" Free some space on that disk or raise the quota, or turn off Worktree for this session, then try again. "
	const head, failed = "Error downloading object: n.bin (x): Smudge error: Error downloading n.bin (x): ", "\nfatal: n.bin: smudge filter lfs failed\n"
	for _, row := range []struct {
		name, stderr string
		rule         *filterRule
		start, code  string
	}{
		{"no space", head + "write " + st + "/tmp/123456: no space left on device" + failed, store, noSpace, "worktree_no_space"},
		{"no space, upper case", head + "write " + st + "/tmp/123456: No space left on device" + failed, store, noSpace, "worktree_no_space"},
		{"quota", head + "write " + st + "/tmp/123456: disk quota exceeded" + failed, store, quota, "worktree_quota"},
		{"quota, upper case", head + "write " + st + "/tmp/123456: Disk quota exceeded" + failed, store, quota, "worktree_quota"},
		{"an object temp", "Error downloading object: f.bin (x): Smudge error: write " + st + "/objects/e1/5f/x.tmp: no space left on device" + failed, store, noSpace, "worktree_no_space"},
		{"a later line counts", head + "write " + st + "/a: permission denied\n" + head + "write " + st + "/b: disk quota exceeded" + failed, store, quota, "worktree_quota"},
		{"no download words", "Smudge error: write " + st + "/tmp/1: no space left on device" + failed, store, "", ""},
		{"words before the end", head + "write " + st + "/tmp/1: no space left on device, and more" + failed, store, "", ""},
		{"another path", head + "write /var/tmp/other/123456: no space left on device" + failed, store, "", ""},
		{"a quoted path", head + "write \"" + st + "/tmp/123456\": no space left on device" + failed, store, "", ""},
		{"an empty path", head + "write : no space left on device" + failed, store, "", ""},
		{"the store itself", head + "write " + st + ": no space left on device" + failed, store, "", ""},
		{"a folder beside the store", head + "write " + st + "x/tmp/1: no space left on device" + failed, store, "", ""},
		{"other words", head + "write " + st + "/tmp/1: permission denied" + failed, store, "", ""},
		{"no store", head + "write " + st + "/tmp/1: no space left on device" + failed, &filterRule{shadow: true}, "", ""},
		{"no settings copy", head + "write " + st + "/tmp/1: no space left on device" + failed, &filterRule{store: store.store}, "", ""},
		{"no rule", head + "write " + st + "/tmp/1: no space left on device" + failed, nil, "", ""},
	} {
		t.Run(row.name, func(t *testing.T) {
			if start, code := storeFullStart(row.stderr, row.rule); start != row.start || code != row.code {
				t.Errorf("storeFullStart = %q, %q, want %q, %q", start, code, row.start, row.code)
			}
		})
	}
}
