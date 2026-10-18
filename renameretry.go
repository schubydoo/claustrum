package main

import "time"

// renameRetryBound is the time from the first try in which a held rename is
// tried again (retryRename). On 5fd08069 a held copy of the shadow folder
// fails at 2047 to 2107 ms. A held staging folder lives about 2.0 s in 8 of 8
// runs (round task009-01, Windows VM). A test replaces it.
var renameRetryBound = 2 * time.Second

// renameRetryMaxPause bounds the pause between two tries. The pauses are
// claustrum's own.
const renameRetryMaxPause = 50 * time.Millisecond

// retryRename runs rename. On Windows it runs it again while it fails because
// another process holds the entry (renameHeld), for at most renameRetryBound
// from the first try. Two renames have it: the copy write of the shadow folder
// and the move of a staging folder. No request deadline and no git-timeout
// ends it. The last error goes back unchanged. On Linux and macOS rename runs
// once.
func retryRename(rename func() error) error {
	return retryWhile(renameRetryBound, renameHeld, rename)
}

// retryWhile runs op until it succeeds, until again is false for its error, or
// until bound has passed since the first try. The pauses grow from 2 ms to
// renameRetryMaxPause.
func retryWhile(bound time.Duration, again func(error) bool, op func() error) error {
	start := time.Now()
	pause := 2 * time.Millisecond
	for {
		err := op()
		if err == nil || !again(err) {
			return err
		}
		left := bound - time.Since(start)
		if left <= 0 {
			return err
		}
		time.Sleep(min(pause, left))
		pause = min(2*pause, renameRetryMaxPause)
	}
}
