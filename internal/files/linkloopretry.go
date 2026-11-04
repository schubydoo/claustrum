package files

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// statPath and openListDir are the first calls of files.stat, files.validate
// and files.list. Tests replace them to answer ELOOP.
var (
	statPath    = os.Stat
	openListDir = os.OpenFile
)

// linkLoopPause is the wait between two tries of retryLinkLoop.
var (
	linkLoopPause = 10 * time.Millisecond
	sleepLinkLoop = time.Sleep
)

// retryLinkLoop runs try again after an ELOOP, up to linkLoopTries in all, with
// linkLoopPause between two tries. Any other answer ends it at once. After the
// last ELOOP it returns that error, so the text stays the one of a single try.
// b3081b51 makes 4 tries about 10 ms apart on Linux (measured on Linux). A later
// try that fails with another error returns that error (claustrum's own, not
// measured). No pause follows the last try (not measured).
func retryLinkLoop[T any](try func() (T, error)) (T, error) {
	v, err := try()
	for n := 1; n < linkLoopTries && errors.Is(err, syscall.ELOOP); n++ {
		sleepLinkLoop(linkLoopPause)
		v, err = try()
	}
	return v, err
}
