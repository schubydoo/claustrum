package git

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// The stop of a checkout whose Git LFS download leaves under a tenth of the
// free space at the start on the disk of its private store
// (worktree_lfs_no_space). 4c111e86 does so on Linux (measured):
//
//   - While the read-tree call runs, a poll every 0.5 s reads the free space of
//     the disk that holds the private store <P>/lfs.
//   - It stops the checkout when `<P>/lfs/incomplete` exists and the free space
//     is under a tenth of the free space at the start. 229 MB and 4127 MB free
//     at the start gave bounds of 22.9 MB and 412.7 MB. An empty `incomplete`
//     folder counts.
//   - The stop is one SIGKILL to the process group of the read-tree call.
//     Then the rollback of a failed checkout runs, and the private folder goes
//     before the reply. The objects that git-lfs fetched are not kept.
//   - A private store on another disk than the one that fills gives no stop.
//
// Claustrum's own, not measured: the first read of the free space comes right
// after the start of the read-tree call, and the bound has no cap. A free space
// that cannot be read gives no stop. A start with 0 free gives no stop. The
// watch runs on macOS too, and Windows has no private store (storeInCheckout).

// lfsRoomPoll is the time between two reads of the free space (measured: 0.5 s).
// A test shrinks it.
var lfsRoomPoll = 500 * time.Millisecond

// lfsRoomWatch is the watch of one read-tree call. dir is the `.checkout-<n>`
// folder, and store is <dir>/lfs.
type lfsRoomWatch struct {
	dir, store string

	mu   sync.Mutex
	done bool
	// fired and free are the result of the poll. Read them only after end.
	fired bool
	free  uint64
	quit  chan struct{}
	ended chan struct{}
}

// newLFSRoomWatch is the watch of the private store store, whose folder is dir.
func newLFSRoomWatch(dir, store string) *lfsRoomWatch {
	w := &lfsRoomWatch{dir: dir, store: store, quit: make(chan struct{}), ended: make(chan struct{})}
	lfsRoomWatchMade(w)
	return w
}

// lfsRoomWatchMade sees each new watch. A test replaces it.
var lfsRoomWatchMade = func(*lfsRoomWatch) {}

// start reads the free space at the start and runs the poll for the git
// process p, the leader of its own process group. It runs right after the
// start of p.
func (w *lfsRoomWatch) start(p *os.Process) {
	free0, ok := storeFreeBytes(w.dir)
	if !ok {
		close(w.ended)
		return
	}
	bound := free0 / 10
	go func() {
		defer close(w.ended)
		tick := time.NewTicker(lfsRoomPoll)
		defer tick.Stop()
		for {
			select {
			case <-w.quit:
				return
			case <-tick.C:
			}
			if _, err := os.Lstat(filepath.Join(w.store, "incomplete")); err != nil {
				continue
			}
			free, ok := storeFreeBytes(w.dir)
			if !ok || free >= bound {
				continue
			}
			w.mu.Lock()
			// The kill goes only to the group of this read-tree call, under the
			// lock of the watch, and only while its leader runs (gitStillRuns).
			// The leader is not reaped here: the wait of exec.Cmd starts only
			// after end (awaitLeaderExit).
			if !w.done && gitStillRuns(p) {
				reapCheckoutGroup(p)
				w.fired, w.free = true, free
			}
			w.mu.Unlock()
			return
		}
	}()
}

// end stops the poll and waits for its goroutine. It runs after the exit of
// the read-tree call and before its reap (awaitLeaderExit). After it, fired
// reports a stop, and free is the free space that the stop read.
func (w *lfsRoomWatch) end() {
	w.mu.Lock()
	w.done = true
	w.mu.Unlock()
	close(w.quit)
	<-w.ended
}

// lfsRoomStop is the error of a read-tree call that the watch stopped. err is
// the error of its wait. runWorktreeCheckout fills the other fields: runDir is
// the folder of the socket, fetched the bytes in the private store at the stop,
// and removeStore the remove of the private folder, which runs once.
type lfsRoomStop struct {
	err         error
	free        uint64
	fetched     uint64
	runDir      string
	removeStore func()
}

func (e *lfsRoomStop) Error() string { return e.err.Error() }

func (e *lfsRoomStop) Unwrap() error { return e.err }

// lfsRoomText is the text of worktree_lfs_no_space (4c111e86, measured on
// Linux).
func lfsRoomText(e *lfsRoomStop) string {
	return "Claude stopped the checkout when only " + roomWords(e.free, false) +
		" was left free on the disk that holds " + e.runDir +
		" on the remote machine. The worktree and the " + roomWords(e.fetched, true) +
		" of Git LFS files already fetched into that folder were removed." +
		" Free up space on that disk, or turn off Worktree for this session, then try again." +
		" Claude keeps a checkout's Git LFS files in that folder until the checkout ends, so they need room on that disk."
}

// roomWords is n bytes in the text of lfsRoomText. Under 1 MiB it is whole
// KiB, under 1 GiB whole MiB, else GiB with one decimal. up rounds up, else
// down. The rows show `4 KiB` to `390 MiB` free and `197 MiB` to `3.5 GiB`
// fetched. A fetched 200.0 MiB shows as `201 MiB` (measured on Linux).
// Claustrum's own, not measured: the rounding down of the free space (the rows
// also fit the nearest value), the rounding of the KiB form, the bounds of
// 1 MiB and 1 GiB, and the form of a free space of 1 GiB or more.
func roomWords(n uint64, up bool) string {
	const kib, mib, gib = 1 << 10, 1 << 20, 1 << 30
	div := func(a, b uint64) uint64 {
		if up {
			return (a + b - 1) / b
		}
		return a / b
	}
	if n < mib {
		return strconv.FormatUint(div(n, kib), 10) + " KiB"
	}
	if n < gib {
		return strconv.FormatUint(div(n, mib), 10) + " MiB"
	}
	tenths := div(n*10, gib)
	return strconv.FormatUint(tenths/10, 10) + "." + strconv.FormatUint(tenths%10, 10) + " GiB"
}

// storeBytes is the sum of the sizes of the regular files below store. The walk
// follows no link. It is the size of the fetched files in lfsRoomText. The kind
// of size (the length of each file) is claustrum's own, not measured.
func storeBytes(store string) uint64 {
	var n uint64
	_ = filepath.WalkDir(store, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		if fi, err := d.Info(); err == nil && fi.Size() > 0 {
			n += uint64(fi.Size())
		}
		return nil
	})
	return n
}
