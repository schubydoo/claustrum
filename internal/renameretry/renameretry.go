package renameretry

import "time"

// Bound is the time from the first try in which a held rename is
// tried again (Retry). On 5fd08069 a held copy of the shadow folder
// fails at 2047 to 2107 ms. A held staging folder lives about 2.0 s in 8 of 8
// runs (measured on Windows). A test replaces it.
var Bound = 2 * time.Second

// MaxPause bounds the pause between two tries. The pauses are
// claustrum's own.
const MaxPause = 50 * time.Millisecond

// Retry runs rename. On Windows it runs it again while it fails because
// another process holds the entry (Held), for at most Bound
// from the first try. Two renames have it: the copy write of the shadow folder
// and the move of a staging folder. No request deadline and no time limit of
// a git call ends it. The last error goes back unchanged. On Linux and macOS rename runs
// once.
func Retry(rename func() error) error {
	return While(Bound, Held, rename)
}

// While runs op until it succeeds, until again is false for its error, or
// until bound has passed since the first try. The pauses grow from 2 ms to
// MaxPause.
func While(bound time.Duration, again func(error) bool, op func() error) error {
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
		pause = min(2*pause, MaxPause)
	}
}
