package renameretry

import (
	"errors"
	"os"
	"testing"
	"time"
)

// TestRenameRetryBoundIsTwoSeconds pins the production bound of the retry
// (measured on Windows) and claustrum's own longest pause.
func TestRenameRetryBoundIsTwoSeconds(t *testing.T) {
	if Bound != 2*time.Second {
		t.Errorf("Bound = %v, want 2s", Bound)
	}
	if MaxPause != 50*time.Millisecond {
		t.Errorf("MaxPause = %v, want 50ms", MaxPause)
	}
}

// TestRetryWhile pins the loop of the retry. A held error is tried again until
// the op succeeds. Another error goes back at once. An op that stays held
// returns its last error once the bound has passed.
func TestRetryWhile(t *testing.T) {
	errHeld := errors.New("held")
	held := func(err error) bool { return errors.Is(err, errHeld) }

	t.Run("released", func(t *testing.T) {
		calls := 0
		err := While(time.Second, held, func() error {
			if calls++; calls < 4 {
				return errHeld
			}
			return nil
		})
		if err != nil || calls != 4 {
			t.Errorf("While = %v after %d calls, want nil after 4", err, calls)
		}
	})
	t.Run("another error", func(t *testing.T) {
		other := errors.New("other")
		calls := 0
		err := While(time.Second, held, func() error { calls++; return other })
		if err != other || calls != 1 {
			t.Errorf("While = %v after %d calls, want the error after 1", err, calls)
		}
	})
	t.Run("held past the bound", func(t *testing.T) {
		const bound = 60 * time.Millisecond
		var last error
		calls := 0
		start := time.Now()
		err := While(bound, held, func() error {
			calls++
			if calls > 10000 {
				return nil
			}
			last = &os.LinkError{Op: "renameat", Old: "a", New: "b", Err: errHeld}
			return last
		})
		took := time.Since(start)
		if err == nil || err != last || calls < 3 {
			t.Errorf("While = %v after %d calls, want the last held error after some tries", err, calls)
		}
		if took < bound || took > bound+time.Second {
			t.Errorf("While took %v, want about %v", took, bound)
		}
	})
}
