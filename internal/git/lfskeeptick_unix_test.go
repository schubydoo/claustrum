//go:build !windows

package git

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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

// watchTickers records each new keepTicker for one test.
func watchTickers(t *testing.T) func() []*keepTicker {
	t.Helper()
	var mu sync.Mutex
	var made []*keepTicker
	old := keepTickerMade
	keepTickerMade = func(k *keepTicker) {
		mu.Lock()
		made = append(made, k)
		mu.Unlock()
	}
	t.Cleanup(func() { keepTickerMade = old })
	return func() []*keepTicker {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(made)
	}
}

// TestKeepTickerEndsAfterAPanic: a panic right after the wait for the
// read-tree call still ends the ticker of the create (claustrum's own).
// Mutation: call endKeeper at the normal return of hardenedGitCheckout only,
// not by defer (the ticker still runs after the panic).
func TestKeepTickerEndsAfterAPanic(t *testing.T) {
	seamFreeSpace(t)
	setKeepTick(t, 20*time.Millisecond)
	made := watchTickers(t)
	old := checkoutWaited
	checkoutWaited = func(*exec.Cmd) { panic("a test panic after the wait") }
	t.Cleanup(func() { checkoutWaited = old })
	f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object"})
	f.onServer(t, "a.bin")
	panicked := make(chan any, 1)
	go func() {
		defer func() { panicked <- recover() }()
		_, _ = gitDispatchErr(f.d, f.createLine(t, "w1"))
	}()
	select {
	case r := <-panicked:
		if r == nil {
			t.Fatal("the create did not panic")
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the create did not end in 60 s")
	}
	got := made()
	if len(got) != 1 {
		t.Fatalf("the create made %d tickers, want 1", len(got))
	}
	select {
	case <-got[0].ended:
	default:
		t.Error("the ticker still runs after the panic")
		got[0].end()
	}
}

// TestKeepTookCountsTheWaitForTheTicks: a keep of a tick that still runs when
// git exits delays the end of the checkout. That wait counts as a time of the
// keep, so a deadline that passes during it gives success, as a deadline
// during the keep after the checkout does (keepWithinDeadline, claustrum's
// own). The first copy of a keep lets b.bin download and then takes 4.5 s,
// with timeoutMs 4000. Mutation: leave tickWait out of keepTook in
// runWorktreeCheckout (the reply is a timeout).
func TestKeepTookCountsTheWaitForTheTicks(t *testing.T) {
	seamFreeSpace(t)
	setKeepTick(t, 20*time.Millisecond)
	made := watchTickers(t)
	f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object", "b.bin": "beta-object"})
	f.onServer(t, "a.bin")
	f.onServer(t, "b.bin")
	release := f.holdDownload(t, "b.bin")
	prevBudget, prevCopy := keepBudget, keepCopy
	keepBudget = 10 * time.Second
	var once sync.Once
	keepCopy = func(w io.Writer, r io.Reader) (int64, error) {
		once.Do(func() {
			release()
			time.Sleep(4500 * time.Millisecond)
		})
		return io.Copy(w, r)
	}
	t.Cleanup(func() { keepBudget, keepCopy = prevBudget, prevCopy })
	raw, _ := f.create(t, map[string]any{"timeoutMs": 4000})
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s, want success", raw)
	}
	if len(made()) != 1 {
		t.Fatalf("the create made %d tickers, want 1", len(made()))
	}
	for _, name := range []string{"a.bin", "b.bin"} {
		if !exists(storeObjectPath(filepath.Join(f.gitDir, "lfs"), f.oids[name])) {
			t.Errorf("%s is not in the store of the repository", name)
		}
	}
}

// TestKeepTickPanicEndsOnlyTheTicks: a panic in a keep of a tick ends the
// ticks with a log line. The create goes on, and the keep after the checkout
// keeps both objects (claustrum's own). Mutation: drop the recover in the
// goroutine of startKeepTicker (the test binary ends).
func TestKeepTickPanicEndsOnlyTheTicks(t *testing.T) {
	seamFreeSpace(t)
	setKeepTick(t, 20*time.Millisecond)
	made := watchTickers(t)
	f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object", "b.bin": "beta-object"})
	f.onServer(t, "a.bin")
	f.onServer(t, "b.bin")
	release := f.holdDownload(t, "b.bin")
	prev := keepCopy
	var once sync.Once
	keepCopy = func(w io.Writer, r io.Reader) (int64, error) {
		once.Do(func() {
			release()
			panic("a test panic in the keep of a tick")
		})
		return io.Copy(w, r)
	}
	t.Cleanup(func() { keepCopy = prev })
	f.wantSuccess(t)
	if got := made(); len(got) != 1 {
		t.Fatalf("the create made %d tickers, want 1", len(got))
	}
	for _, name := range []string{"a.bin", "b.bin"} {
		if !exists(storeObjectPath(filepath.Join(f.gitDir, "lfs"), f.oids[name])) {
			t.Errorf("%s is not in the store of the repository", name)
		}
	}
}

// stalledHash is a hash whose first Write runs first, then waits for done or for
// 10 s, and reports in stopped whether done ended the wait.
type stalledHash struct {
	hash.Hash
	first   func()
	done    <-chan struct{}
	stopped *atomic.Bool
}

func (h stalledHash) Write(p []byte) (int, error) {
	h.first()
	select {
	case <-h.done:
		h.stopped.Store(true)
	case <-time.After(10 * time.Second):
	}
	return h.Hash.Write(p)
}

// TestLFSKeepTickStopsAtTheExitOfGit: a keep of a tick that still reads its
// object when the read-tree call exits stops there, with no copy. The keep
// after the checkout reads the object again and makes its one copy. On
// 4c111e86 the first read of a 900 MiB object ended at the exit of git in 10
// of 10 rows. One temp file followed after the checkout (measured on Linux).
// The stall holds only a keep of a tick while the download of b.bin is held,
// so it holds a.bin. The copy of a.bin must come after the end of the ticks.
// Mutations: drop the stop of the context of the ticks in keepTicker.end, or
// give the keep of a tick a context from context.Background (in both the
// read of the tick runs on, and the tick makes the copy).
//
// The stall ends when the stop of the ticks has returned, not when ctx is
// done. A cancel closes the done channel of ctx before it ends the context of
// each keep, so a stall that ended at ctx.Done let the tick read on and copy.
func TestLFSKeepTickStopsAtTheExitOfGit(t *testing.T) {
	seamFreeSpace(t)
	setKeepTick(t, 20*time.Millisecond)
	made := watchTickers(t)
	halted := make(chan struct{})
	var haltedOnce sync.Once
	watch := keepTickerMade
	keepTickerMade = func(k *keepTicker) {
		watch(k)
		stop := k.stop
		k.stop = func() {
			stop()
			haltedOnce.Do(func() { close(halted) })
		}
	}
	f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object", "b.bin": "beta-object"})
	f.onServer(t, "a.bin")
	f.onServer(t, "b.bin")
	release := f.holdDownload(t, "b.bin")
	prevHash, prevCopy, prevBudget := newObjectHash, keepCopy, keepBudget
	keepBudget = 30 * time.Second
	var stopped, released atomic.Bool
	var mu sync.Mutex
	copiesA, copiesInTick := 0, 0
	tickEnded := func() bool {
		got := made()
		if len(got) != 1 {
			return false
		}
		select {
		case <-got[0].ended:
			return true
		default:
			return false
		}
	}
	newObjectHash = func() hash.Hash {
		got := made()
		if len(got) != 1 || tickEnded() || released.Load() {
			return sha256.New()
		}
		var once sync.Once
		free := func() {
			once.Do(func() {
				released.Store(true)
				release()
			})
		}
		return stalledHash{Hash: sha256.New(), first: free, done: halted, stopped: &stopped}
	}
	keepCopy = func(w io.Writer, r io.Reader) (int64, error) {
		if file, ok := w.(*os.File); ok && strings.HasSuffix(file.Name(), f.oids["a.bin"]) {
			inTick := !tickEnded()
			mu.Lock()
			copiesA++
			if inTick {
				copiesInTick++
			}
			mu.Unlock()
		}
		return io.Copy(w, r)
	}
	t.Cleanup(func() { newObjectHash, keepCopy, keepBudget = prevHash, prevCopy, prevBudget })
	f.wantSuccess(t)
	if !stopped.Load() {
		t.Error("the keep of the tick still read a.bin after the exit of git")
	}
	mu.Lock()
	got, inTick := copiesA, copiesInTick
	mu.Unlock()
	if got != 1 || inTick != 0 {
		t.Errorf("the keep copied a.bin %d times, %d of them in a tick, want 1 after the checkout", got, inTick)
	}
	if !exists(storeObjectPath(filepath.Join(f.gitDir, "lfs"), f.oids["a.bin"])) {
		t.Error("a.bin is not in the store of the repository")
	}
}

// TestLFSKeepTickStopsBeforeTheDrain: a child of the download of b.bin holds
// the stderr of git for 1 s after git exits, so the drain of the checkout
// waits. A keep of a tick that still reads a.bin at the exit of git must not
// copy it during that drain. The keep after the checkout makes the one copy
// (4c111e86 ends that read at the exit of git, measured on Linux). Mutation:
// drop both calls of exited in runCheckoutPipes (the keep of the tick reads on
// through the drain and copies a.bin).
func TestLFSKeepTickStopsBeforeTheDrain(t *testing.T) {
	seamFreeSpace(t)
	setKeepTick(t, 20*time.Millisecond)
	made := watchTickers(t)
	f := newLFSFixture(t, map[string]string{"a.bin": "alpha-object", "b.bin": "beta-object"})
	f.onServer(t, "a.bin")
	f.onServer(t, "b.bin")
	mustWrite(t, filepath.Join(f.remote, f.oids["b.bin"]+".linger"), "")
	release := f.holdDownload(t, "b.bin")
	waited := make(chan struct{})
	var waitedOnce sync.Once
	prevWaited := checkoutWaited
	checkoutWaited = func(*exec.Cmd) { waitedOnce.Do(func() { close(waited) }) }
	prevHash, prevCopy, prevBudget := newObjectHash, keepCopy, keepBudget
	keepBudget = 30 * time.Second
	var stopped, released atomic.Bool
	var mu sync.Mutex
	copiesA, copiesInTick := 0, 0
	tickEnded := func() bool {
		got := made()
		if len(got) != 1 {
			return false
		}
		select {
		case <-got[0].ended:
			return true
		default:
			return false
		}
	}
	newObjectHash = func() hash.Hash {
		if len(made()) != 1 || tickEnded() || released.Load() {
			return sha256.New()
		}
		var once sync.Once
		free := func() {
			once.Do(func() {
				released.Store(true)
				release()
			})
		}
		// The read of a.bin waits for the exit of git, then goes on.
		return stalledHash{Hash: sha256.New(), first: free, done: waited, stopped: &stopped}
	}
	keepCopy = func(w io.Writer, r io.Reader) (int64, error) {
		if file, ok := w.(*os.File); ok && strings.HasSuffix(file.Name(), f.oids["a.bin"]) {
			inTick := !tickEnded()
			mu.Lock()
			copiesA++
			if inTick {
				copiesInTick++
			}
			mu.Unlock()
		}
		return io.Copy(w, r)
	}
	t.Cleanup(func() {
		newObjectHash, keepCopy, keepBudget, checkoutWaited = prevHash, prevCopy, prevBudget, prevWaited
	})
	f.wantSuccess(t)
	if !stopped.Load() {
		t.Fatal("the read of a.bin in a tick did not wait for the exit of git")
	}
	mu.Lock()
	got, inTick := copiesA, copiesInTick
	mu.Unlock()
	if got != 1 || inTick != 0 {
		t.Errorf("the keep copied a.bin %d times, %d of them in a tick, want 1 after the checkout", got, inTick)
	}
	if !exists(storeObjectPath(filepath.Join(f.gitDir, "lfs"), f.oids["a.bin"])) {
		t.Error("a.bin is not in the store of the repository")
	}
}

// TestKeepTickEveryTwoSeconds: the ticks of the keep come every 2 s, and a
// ticker with no tick keeps nothing, so no pass runs at time zero. On 4c111e86
// the passes came at 2 s, 4 s and 6 s (measured on Linux and macOS). A second
// ticker gets two ticks and keeps the object, so the test can see a keep.
// Mutations: set keepTickEvery to time.Second, or run one pass before the loop
// of startKeepTicker.
func TestKeepTickEveryTwoSeconds(t *testing.T) {
	seamFreeSpace(t)
	var period time.Duration
	ticks := make(chan time.Time)
	old := newKeepTick
	newKeepTick = func(d time.Duration) (<-chan time.Time, func()) {
		period = d
		return ticks, func() {}
	}
	t.Cleanup(func() { newKeepTick = old })
	gitDir := filepath.Join(t.TempDir(), ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(gitDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	info, err := root.Stat(".")
	if err != nil {
		t.Fatal(err)
	}
	u := gitUse{gitDir: gitDir, accepted: &acceptedGitDir{path: gitDir, root: root, info: info}}
	private := filepath.Join(t.TempDir(), "lfs")
	body := "alpha-object"
	sum := sha256.Sum256([]byte(body))
	oid := hex.EncodeToString(sum[:])
	mustWrite(t, storeObjectPath(private, oid), body)
	kept := storeObjectPath(filepath.Join(gitDir, "lfs"), oid)

	startKeepTicker(u, private).end()
	if period != 2*time.Second {
		t.Errorf("the keep ticks every %v, want 2s", period)
	}
	if exists(kept) {
		t.Fatal("a ticker with no tick kept the object")
	}
	k := startKeepTicker(u, private)
	// The second send waits for the end of the pass of the first.
	ticks <- time.Now()
	ticks <- time.Now()
	k.end()
	if !exists(kept) {
		t.Error("a ticker with two ticks did not keep the object")
	}
}
