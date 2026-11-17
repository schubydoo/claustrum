package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// stallReleaseCloses makes each close of the release wait until the returned free
// runs. Each closed file goes to closed.
func stallReleaseCloses(t *testing.T) (closed chan *os.File, free func()) {
	t.Helper()
	hold := make(chan struct{})
	closed = make(chan *os.File, 16)
	old := closeReleasedTempRoot
	closeReleasedTempRoot = func(f *os.File) {
		<-hold
		_ = f.Close()
		closed <- f
	}
	freed := false
	free = func() {
		if !freed {
			freed = true
			close(hold)
		}
	}
	t.Cleanup(func() {
		free()
		closeReleasedTempRoot = old
	})
	return closed, free
}

// wantReleaseOnTime runs release and fails the test if it waits for a close of a
// lock file, as on a stuck mount (claustrum's own, not measured).
func wantReleaseOnTime(t *testing.T, release func(), free func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		release()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		free()
		<-done
		t.Fatal("the release waited for the close of the lock file")
	}
}

func wantClosed(t *testing.T, closed chan *os.File, want *os.File) {
	t.Helper()
	select {
	case got := <-closed:
		if got != want {
			t.Errorf("closed %v, want the lock file %v", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the lock file was not closed")
	}
}

// The release of a lost folder closes its lock file, but it does not wait for that
// close (claustrum's own, not measured).
func TestCLITempRootReleaseOfALostFolderDoesNotWaitForTheClose(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	setTempRootCheckEvery(t, 20*time.Millisecond)
	x, runDir := tempRootDir(t, true)
	a := runTempRootBound(t, x, runDir, &rel, nil)
	h := heldRecordOf(x, a.Name)
	f := filepath.Join(x, a.Name)
	if err := os.Rename(f, f+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f+".old", f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "loss", h.lost.Load)
	tempRoots.mu.Lock()
	lockFile := h.f
	tempRoots.mu.Unlock()
	closed, free := stallReleaseCloses(t)
	wantReleaseOnTime(t, rel[0], free)
	free()
	wantClosed(t, closed, lockFile)
}

// The release that gives back one folder more than tempRootGivenKeep closes the lock
// file of the oldest given-back folder, but it does not wait for that close
// (claustrum's own, not measured).
func TestCLITempRootReleaseOverTheKeepDoesNotWaitForTheClose(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	var first *os.File
	for i := 0; i <= tempRootGivenKeep; i++ {
		clearTempRootMakes()
		x, runDir := tempRootDir(t, true)
		a := runTempRootBound(t, x, runDir, &rel, nil)
		if a == nil || a.Name == "" {
			t.Fatalf("spawn %d: answer %+v, want a folder", i, a)
		}
		if i == 0 {
			first = heldRecordOf(x, a.Name).f
		}
		if i < tempRootGivenKeep {
			rel[i]()
			waitTempRootsLive(t, 0)
		}
	}
	tempRoots.mu.Lock()
	given := len(tempRoots.given)
	tempRoots.mu.Unlock()
	if given != tempRootGivenKeep {
		t.Fatalf("%d given-back folders, want %d", given, tempRootGivenKeep)
	}
	closed, free := stallReleaseCloses(t)
	wantReleaseOnTime(t, rel[tempRootGivenKeep], free)
	free()
	wantClosed(t, closed, first)
}
