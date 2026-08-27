package main

import (
	"runtime"
	"sync"
	"testing"
)

// withWorktreeRepoLock must make same-repo calls mutually exclusive. A non-atomic
// read-modify-write inside the critical section stays consistent only if the lock
// serialises; without it the update is lost (and -race flags the data race).
func TestWithWorktreeRepoLockSerializesSameRepo(t *testing.T) {
	const repo = "/tmp/some/repo"
	const n = 100
	var counter int
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			withWorktreeRepoLock(repo, func() response {
				c := counter
				runtime.Gosched() // widen the window a lost update would need
				counter = c + 1
				return response{}
			})
		})
	}
	wg.Wait()
	if counter != n {
		t.Errorf("counter = %d, want %d — the lock did not serialise same-repo calls", counter, n)
	}
}

// Different repositories take different mutexes, so their operations are not
// serialised against each other — a smoke check that distinct keys do not deadlock
// or share a lock.
func TestWithWorktreeRepoLockDistinctReposDoNotShare(t *testing.T) {
	done := make(chan struct{})
	go func() {
		withWorktreeRepoLock("/tmp/repo-a", func() response {
			withWorktreeRepoLock("/tmp/repo-b", func() response { return response{} })
			return response{}
		})
		close(done)
	}()
	<-done // completes only if the two repos use independent locks (no self-deadlock)
}
