//go:build !windows

package git

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// setKeepTick sets keepTickEvery for one test.
func setKeepTick(t *testing.T, d time.Duration) {
	t.Helper()
	old := keepTickEvery
	keepTickEvery = d
	t.Cleanup(func() { keepTickEvery = old })
}

// TestKeepTickerEndsWithTheCheckout: each keepTicker of a create ends before
// its reply, so no tick runs after it (claustrum's own). Mutation: replace
// `defer keeper.end()` in hardenedGitCheckout by `_ = keeper` (the ticker
// still runs after the reply).
func TestKeepTickerEndsWithTheCheckout(t *testing.T) {
	seamFreeSpace(t)
	setKeepTick(t, 20*time.Millisecond)
	var mu sync.Mutex
	var made []*keepTicker
	old := keepTickerMade
	keepTickerMade = func(k *keepTicker) {
		mu.Lock()
		made = append(made, k)
		mu.Unlock()
	}
	t.Cleanup(func() { keepTickerMade = old })
	f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object"})
	f.onServer(t, "a.bin")
	f.wantSuccess(t)
	mu.Lock()
	defer mu.Unlock()
	if len(made) != 1 {
		t.Fatalf("the create made %d tickers, want 1", len(made))
	}
	select {
	case <-made[0].ended:
	default:
		t.Error("the ticker of the create still runs after its reply")
	}
	// A second end only waits.
	made[0].end()
}

// TestLFSKeepTickDuringCheckout: an object that git-lfs downloaded reaches the
// store of the repository while the checkout still runs. On 4c111e86 a.bin
// came about 1 s after its download, before b.bin was done, and b.bin at the
// reply (measured on Linux). The keep of a tick makes no sweep, so a stale
// temp name of another object stays in the shard (claustrum's own, not
// measured).
//
// Mutations: start no keepTicker in hardenedGitCheckout (a.bin waits for the
// end of the checkout). Keep with keepAfterGood in the tick (the stale name
// goes).
func TestLFSKeepTickDuringCheckout(t *testing.T) {
	seamFreeSpace(t)
	setKeepTick(t, 50*time.Millisecond)
	f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object", "b.bin": "beta-object"})
	f.onServer(t, "a.bin")
	f.onServer(t, "b.bin")
	release := f.holdDownload(t, "b.bin")
	store := filepath.Join(f.gitDir, "lfs")
	if err := os.MkdirAll(f.shard("a.bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(f.shard("a.bin"), ".claude-0123abcd-"+strings.Repeat("e", 64))
	mustWrite(t, stale, "x")
	old := time.Now().Add(-12 * time.Minute)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	created := dispatchAsync(f.d, f.createLine(t, "w1"))
	keptA := storeObjectPath(store, f.oids["a.bin"])
	if !pollUntil(20*time.Second, func() bool { return exists(keptA) }) {
		release()
		t.Fatal("a.bin did not reach the store of the repository while the download of b.bin was held")
	}
	if exists(storeObjectPath(store, f.oids["b.bin"])) {
		t.Error("b.bin is in the store of the repository before its download")
	}
	if !exists(stale) {
		t.Error("the keep during the checkout removed a stale temp name of another object")
	}
	release()
	if raw := receive(t, "the create", created); !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	for _, name := range []string{"a.bin", "b.bin"} {
		if b, err := os.ReadFile(storeObjectPath(store, f.oids[name])); err != nil || string(b) != string(f.bodies[name]) {
			t.Errorf("%s in the store of the repository = %q, %v, want it kept", name, b, err)
		}
	}
}
