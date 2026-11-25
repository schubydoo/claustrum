//go:build !windows

package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setKeepRules sets the two keep rules of macOS for one test.
func setKeepRules(t *testing.T, stops, sums bool) {
	t.Helper()
	prevStops, prevSums := keepStopsAtMiss, keepSumsCopy
	keepStopsAtMiss, keepSumsCopy = stops, sums
	t.Cleanup(func() { keepStopsAtMiss, keepSumsCopy = prevStops, prevSums })
}

// setKeepFree makes the free space of every folder free for one test.
func setKeepFree(t *testing.T, free *uint64) {
	t.Helper()
	prev := storeFreeBytes
	storeFreeBytes = func(string) (uint64, bool) { return *free, true }
	t.Cleanup(func() { storeFreeBytes = prev })
}

// keepStage is a held git directory and a private store with objects.
type keepStage struct {
	u       gitUse
	private string
	store   string
}

func newKeepStage(t *testing.T) keepStage {
	t.Helper()
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
	return keepStage{
		u:       gitUse{gitDir: gitDir, accepted: &acceptedGitDir{path: gitDir, root: root, info: info}, keeps: &keepPass{}},
		private: filepath.Join(t.TempDir(), "lfs"),
		store:   filepath.Join(gitDir, "lfs"),
	}
}

// add puts an object with the body body into the private store.
func (s keepStage) add(t *testing.T, body string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(body))
	oid := hex.EncodeToString(sum[:])
	mustWrite(t, storeObjectPath(s.private, oid), body)
	return oid
}

func (s keepStage) kept(oid string) bool {
	return exists(storeObjectPath(s.store, oid))
}

// bodyWithOID is a body of size bytes whose oid does or does not sort before
// other.
func bodyWithOID(t *testing.T, size int, other string, before bool) string {
	t.Helper()
	for i := range 100000 {
		head := fmt.Sprintf("%08d", i)
		body := head + strings.Repeat("x", size-len(head))
		sum := sha256.Sum256([]byte(body))
		if (hex.EncodeToString(sum[:]) < other) == before {
			return body
		}
	}
	t.Fatal("no body found")
	return ""
}

// TestKeepEndsAtFirstMiss: one pass walks the objects in oid order. The first
// that fails the test of the space ends the keep, and an object after it stays
// out, with room for it. An object before it is kept (87375cc7 and 4c111e86,
// measured on macOS). With the rule off, as on Linux, the miss is skipped.
//
// Mutation: drop the markMiss call in returnDownloads (the last object is
// kept).
func TestKeepEndsAtFirstMiss(t *testing.T) {
	big := strings.Repeat("b", 4000)
	sum := sha256.Sum256([]byte(big))
	bigOID := hex.EncodeToString(sum[:])
	for _, row := range []struct {
		name     string
		stops    bool
		wantLast bool
	}{
		{"the rule on", true, false},
		{"the rule off", false, true},
	} {
		t.Run(row.name, func(t *testing.T) {
			setKeepRules(t, row.stops, false)
			free := uint64(1<<30 + 100)
			setKeepFree(t, &free)
			s := newKeepStage(t)
			first := s.add(t, bodyWithOID(t, 20, bigOID, true))
			mid := s.add(t, big)
			last := s.add(t, bodyWithOID(t, 21, bigOID, false))
			left := returnDownloads(s.u, s.private, keepAfterGood)
			if !s.kept(first) {
				t.Error("the object before the miss is not kept")
			}
			if s.kept(mid) {
				t.Error("the object without room is kept")
			}
			if s.kept(last) != row.wantLast {
				t.Errorf("the object after the miss: kept %v, want %v", s.kept(last), row.wantLast)
			}
			wantLeft := 2
			if row.wantLast {
				wantLeft = 1
			}
			if left.n != wantLeft {
				t.Errorf("the keep left out %d objects, want %d", left.n, wantLeft)
			}
		})
	}
}

// TestKeepMissHoldsAfterTheSpaceRises: after a miss a later pass keeps nothing,
// also with room for all (measured on macOS with a rise to 1700 MB). An object
// first tested after a rise is kept (measured on macOS, 1 of 3 runs).
//
// Mutation: drop the test of over in returnDownloads (the second pass keeps
// both objects).
func TestKeepMissHoldsAfterTheSpaceRises(t *testing.T) {
	setKeepRules(t, true, false)
	free := uint64(1<<30 + 100)
	setKeepFree(t, &free)
	bigBody := strings.Repeat("b", 4000)
	sum := sha256.Sum256([]byte(bigBody))
	smallBody := bodyWithOID(t, 20, hex.EncodeToString(sum[:]), false)

	s := newKeepStage(t)
	big, small := s.add(t, bigBody), s.add(t, smallBody)
	returnDownloads(s.u, s.private, keepDuringCheckout)
	free = 1 << 40
	returnDownloads(s.u, s.private, keepAfterGood)
	if s.kept(big) || s.kept(small) {
		t.Errorf("after the miss: big kept %v, small kept %v, want neither", s.kept(big), s.kept(small))
	}

	free = 1<<30 + 100
	r := newKeepStage(t)
	early := r.add(t, smallBody)
	returnDownloads(r.u, r.private, keepDuringCheckout)
	free = 1 << 40
	late := r.add(t, bigBody)
	returnDownloads(r.u, r.private, keepAfterGood)
	if !r.kept(early) || !r.kept(late) {
		t.Errorf("with no miss: first kept %v, later kept %v, want both", r.kept(early), r.kept(late))
	}
}

// TestKeepTickMissEndsTheKeep: a miss in a tick ends the ticks and the keep
// after the checkout (measured on macOS). Mutation: drop the markMiss call in
// the goroutine of startKeepTicker (the keep after the checkout keeps the
// object).
func TestKeepTickMissEndsTheKeep(t *testing.T) {
	setKeepRules(t, true, false)
	free := uint64(1<<30 + 100)
	setKeepFree(t, &free)
	ticks := make(chan time.Time)
	started := make(chan struct{}, 1)
	old := newKeepTick
	newKeepTick = func(time.Duration) (<-chan time.Time, func()) {
		started <- struct{}{}
		return ticks, func() {}
	}
	t.Cleanup(func() { newKeepTick = old })
	bigBody := strings.Repeat("b", 4000)
	sum := sha256.Sum256([]byte(bigBody))
	s := newKeepStage(t)
	big, small := s.add(t, bigBody), s.add(t, bodyWithOID(t, 20, hex.EncodeToString(sum[:]), false))
	k := startKeepTicker(s.u, s.private)
	<-started
	ticks <- time.Now()
	select {
	case <-k.ended:
	case <-time.After(20 * time.Second):
		k.end()
		t.Fatal("the ticks did not end after the miss")
	}
	k.end()
	free = 1 << 40
	returnDownloads(s.u, s.private, keepAfterGood)
	if s.kept(big) || s.kept(small) {
		t.Errorf("after a miss in a tick: big kept %v, small kept %v, want neither", s.kept(big), s.kept(small))
	}
}

// TestKeepWalksTheOIDs: a create walks one pass in oid order, not in the order
// of the file names. a.bin is small and b.bin has no room. The oid of b.bin
// sorts first, so the keep ends there and a.bin stays out (87375cc7 and
// 4c111e86, measured on macOS). Mutation: reverse the list in returnDownloads
// (a.bin is kept).
func TestKeepWalksTheOIDs(t *testing.T) {
	setKeepRules(t, true, false)
	big := strings.Repeat("b", 4000)
	sum := sha256.Sum256([]byte(big))
	small := bodyWithOID(t, 20, hex.EncodeToString(sum[:]), false)
	free := uint64(1<<30 + 100)
	setKeepFree(t, &free)
	f := newLFSFixture(t, map[string]string{"a.bin": small, "b.bin": big})
	f.onServer(t, "a.bin")
	f.onServer(t, "b.bin")
	f.wantSuccess(t)
	store := filepath.Join(f.gitDir, "lfs")
	for _, name := range []string{"a.bin", "b.bin"} {
		if exists(storeObjectPath(store, f.oids[name])) {
			t.Errorf("%s is in the store of the repository, want the keep ended at b.bin", name)
		}
	}
}

// TestKeepFailedPassSharesTheBound: after a failed checkout one bound covers
// the pass, in oid order (the rows of 87375cc7 on macOS fit it). Each copy
// takes 60 percent of the bound, so the first object is kept and the second is
// cut in its copy, and its temp file goes (claustrum's own, not measured). A
// first copy that the bound cuts leaves no time for the second. After a good
// checkout and with the rule off each object has its own bound (claustrum's
// own, not measured).
//
// Mutations: give each object context.Background() as the parent of its bound
// in returnDownloads (the second object is kept). Drop the form test of the
// shared bound (the row of a good checkout loses its second object). Force the
// gate on (the row of the rule off loses its second object).
func TestKeepFailedPassSharesTheBound(t *testing.T) {
	bigBody := strings.Repeat("b", 4000)
	sum := sha256.Sum256([]byte(bigBody))
	const bound = 2 * time.Second
	for _, row := range []struct {
		name                string
		on                  bool
		form                keepForm
		hold                time.Duration
		wantFirst, wantLast bool
	}{
		{"a failed checkout", true, keepAfterDeadline, 6 * bound / 10, true, false},
		{"the first copy cut", true, keepAfterDeadline, 3 * bound / 2, false, false},
		{"a good checkout", true, keepAfterGood, 6 * bound / 10, true, true},
		{"the rule off", false, keepAfterDeadline, 6 * bound / 10, true, true},
	} {
		t.Run(row.name, func(t *testing.T) {
			setKeepRules(t, false, false)
			prevPass, prevBudget, prevCopy := keepPassBound, keepBudget, keepCopy
			keepPassBound, keepBudget = row.on, bound
			keepCopy = func(w io.Writer, r io.Reader) (int64, error) {
				time.Sleep(row.hold)
				return io.Copy(w, r)
			}
			t.Cleanup(func() { keepPassBound, keepBudget, keepCopy = prevPass, prevBudget, prevCopy })
			seamFreeSpace(t)
			s := newKeepStage(t)
			first := s.add(t, bigBody)
			last := s.add(t, bodyWithOID(t, 20, hex.EncodeToString(sum[:]), false))
			returnDownloads(s.u, s.private, row.form)
			if s.kept(first) != row.wantFirst {
				t.Errorf("the first object: kept %v, want %v", s.kept(first), row.wantFirst)
			}
			if s.kept(last) != row.wantLast {
				t.Errorf("the second object: kept %v, want %v", s.kept(last), row.wantLast)
			}
			s.noKeepTemp(t, first)
			if exists(filepath.Dir(storeObjectPath(s.store, last))) {
				s.noKeepTemp(t, last)
			}
		})
	}
}

// TestKeepCopyTestsItsBytes: with the rule of macOS the copy computes the
// sha256 of what it reads. Bytes that change after the bytes test fail the
// copy, and its temp file goes. With the rule off, as on Linux, they are kept.
// Good bytes are kept in both forms.
//
// Mutations: drop the compare after keepCopy in copyIntoRoot (the changed bytes
// are kept). Drop the TeeReader (the good copy fails).
func TestKeepCopyTestsItsBytes(t *testing.T) {
	const body = "alpha-object"
	for _, row := range []struct {
		name         string
		sums, change bool
		wantKept     bool
	}{
		{"sum, good bytes", true, false, true},
		{"sum, changed bytes", true, true, false},
		{"no sum, good bytes", false, false, true},
		{"no sum, changed bytes", false, true, true},
	} {
		t.Run(row.name, func(t *testing.T) {
			setKeepRules(t, false, row.sums)
			seamFreeSpace(t)
			s := newKeepStage(t)
			oid := s.add(t, body)
			prev := keepCopy
			keepCopy = func(w io.Writer, r io.Reader) (int64, error) {
				if row.change {
					if err := os.WriteFile(storeObjectPath(s.private, oid), []byte("ALPHA-OBJECT"), 0o644); err != nil {
						t.Error(err)
					}
				}
				return io.Copy(w, r)
			}
			t.Cleanup(func() { keepCopy = prev })
			returnDownloads(s.u, s.private, keepAfterGood)
			if s.kept(oid) != row.wantKept {
				t.Errorf("kept %v, want %v", s.kept(oid), row.wantKept)
			}
			names, err := os.ReadDir(filepath.Dir(storeObjectPath(s.store, oid)))
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range names {
				if strings.HasPrefix(e.Name(), ".claude-") {
					t.Errorf("the temp file %s stayed", e.Name())
				}
			}
		})
	}
}

// tickOnce runs one tick of a new keepTicker of s. It returns the ticker and
// its tick channel. A second send on that channel waits for the end of the
// pass of the first tick.
func tickOnce(t *testing.T, s keepStage) (*keepTicker, chan<- time.Time) {
	t.Helper()
	ticks := make(chan time.Time)
	started := make(chan struct{}, 1)
	old := newKeepTick
	newKeepTick = func(time.Duration) (<-chan time.Time, func()) {
		started <- struct{}{}
		return ticks, func() {}
	}
	t.Cleanup(func() { newKeepTick = old })
	k := startKeepTicker(s.u, s.private)
	<-started
	ticks <- time.Now()
	return k, ticks
}

// noKeepTemp fails on a temp file of the keep in the shard of oid.
func (s keepStage) noKeepTemp(t *testing.T, oid string) {
	t.Helper()
	names, err := os.ReadDir(filepath.Dir(storeObjectPath(s.store, oid)))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range names {
		if strings.HasPrefix(e.Name(), ".claude-") {
			t.Errorf("the temp file %s stayed", e.Name())
		}
	}
}

// pausedHash is a hash whose every write waits 200 ms first.
type pausedHash struct{ hash.Hash }

func (h pausedHash) Write(p []byte) (int, error) {
	time.Sleep(200 * time.Millisecond)
	return h.Hash.Write(p)
}

// TestKeepTickCopyPassesTheBudget: keepBudget cuts neither the bytes test nor
// the copy of a tick on macOS. The uncut copy is measured (87375cc7). No
// bound on the bytes test is claustrum's own, not measured. With
// the rule off, as on Linux, it cuts both. Mutation: drop the branch of
// keepTickRunsToExit in startKeepTicker (no object is kept).
func TestKeepTickCopyPassesTheBudget(t *testing.T) {
	for _, row := range []struct {
		name     string
		on, test bool
	}{
		{"a slow copy, the rule on", true, false},
		{"a slow copy, the rule off", false, false},
		{"a slow bytes test, the rule on", true, true},
		{"a slow bytes test, the rule off", false, true},
	} {
		t.Run(row.name, func(t *testing.T) {
			setKeepRules(t, false, false)
			prevExit, prevBudget, prevCopy, prevHash := keepTickRunsToExit, keepBudget, keepCopy, newObjectHash
			keepTickRunsToExit, keepBudget = row.on, 50*time.Millisecond
			if row.test {
				newObjectHash = func() hash.Hash { return pausedHash{sha256.New()} }
			} else {
				keepCopy = func(w io.Writer, r io.Reader) (int64, error) {
					time.Sleep(200 * time.Millisecond)
					return io.Copy(w, r)
				}
			}
			t.Cleanup(func() {
				keepTickRunsToExit, keepBudget, keepCopy, newObjectHash = prevExit, prevBudget, prevCopy, prevHash
			})
			seamFreeSpace(t)
			s := newKeepStage(t)
			oid := s.add(t, "alpha-object")
			k, ticks := tickOnce(t, s)
			ticks <- time.Now()
			k.end()
			if s.kept(oid) != row.on {
				t.Errorf("kept %v, want %v", s.kept(oid), row.on)
			}
			if exists(filepath.Dir(storeObjectPath(s.store, oid))) {
				s.noKeepTemp(t, oid)
			}
		})
	}
}

// TestKeepTickCopyStopsAtTheExit: the copy of a tick stops at the exit of git
// and its temp file goes (macOS, claustrum's own, not measured). Mutation: give
// the keep of a tick context.Background() in place of k.ctx in
// startKeepTicker (the object is kept).
func TestKeepTickCopyStopsAtTheExit(t *testing.T) {
	setKeepRules(t, false, false)
	prevExit, prevCopy := keepTickRunsToExit, keepCopy
	keepTickRunsToExit = true
	entered, halted := make(chan struct{}), make(chan struct{})
	keepCopy = func(w io.Writer, r io.Reader) (int64, error) {
		close(entered)
		<-halted
		return io.Copy(w, r)
	}
	t.Cleanup(func() { keepTickRunsToExit, keepCopy = prevExit, prevCopy })
	seamFreeSpace(t)
	s := newKeepStage(t)
	oid := s.add(t, "alpha-object")
	k, _ := tickOnce(t, s)
	select {
	case <-entered:
	case <-time.After(20 * time.Second):
		k.end()
		t.Fatal("the copy of the tick did not start")
	}
	k.halt()
	close(halted)
	k.end()
	if s.kept(oid) {
		t.Error("the copy of the tick ran past the exit of git")
	}
	s.noKeepTemp(t, oid)
}

// TestFailedCheckoutKeepsByCopy: after a checkout that failed by itself the
// keep copies on macOS, and makes a hard link with the rule off, as on Linux.
// Mutation: drop keepFailedByCopy from failedKeepForm.
func TestFailedCheckoutKeepsByCopy(t *testing.T) {
	prev := keepFailedByCopy
	t.Cleanup(func() { keepFailedByCopy = prev })
	for _, row := range []struct {
		on   bool
		want keepForm
	}{{true, keepAfterDeadline}, {false, keepAfterFailure}} {
		keepFailedByCopy = row.on
		if got := failedKeepForm(context.Background(), os.ErrInvalid); got != row.want {
			t.Errorf("rule %v: the form is %v, want %v", row.on, got, row.want)
		}
	}
}
