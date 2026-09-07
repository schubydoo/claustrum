package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWireLogChmodFailureIsNonFatal covers newWireLog's chmod arm, which is a
// warning, not a failure: the capture must still open, so a mode the daemon could
// not tighten costs a log line rather than the whole -wire-log surface. No honest
// fixture reaches the arm (the file was just created by this process, so it owns
// it), and the host fixtures that do refuse it are a shared device a CAP_FOWNER
// process would really re-mode (/dev/null) or a file owned by a second user CI does
// not have. The seam fails on a t.TempDir() file instead.
//
// The assertions are on the outcome: no error, a usable log that records a frame,
// and the exact warning naming the path and the reason. A mutant that makes the arm
// fatal fails the first; one that drops the warning fails the last.
func TestWireLogChmodFailureIsNonFatal(t *testing.T) {
	errChmod := errors.New("operation not permitted")
	old := chmodWireLog
	t.Cleanup(func() { chmodWireLog = old })
	chmodWireLog = func(*os.File, os.FileMode) error { return errChmod }

	path := filepath.Join(t.TempDir(), "wire.log")
	var w *wireLog
	logs := captureLog(t, func() {
		var err error
		w, err = newWireLog(path, wireLogMaxString)
		if err != nil {
			t.Fatalf("newWireLog with a failing chmod = %v, want nil (the arm is non-fatal)", err)
		}
	})
	if w == nil {
		t.Fatal("newWireLog returned a nil log with a nil error")
	}
	t.Cleanup(w.Close)
	want := "[WireLog] chmod " + path + " 0600: " + errChmod.Error()
	if !strings.Contains(logs, want) {
		t.Errorf("log missing %q\n--- got ---\n%s", want, logs)
	}
	w.record(1, "in", []byte(`{"jsonrpc":"2.0","id":1,"method":"server.capabilities"}`))
	if recs := readWireLog(t, path); len(recs) != 1 {
		t.Errorf("recorded %d frame(s) after the chmod warning, want 1", len(recs))
	}
}
