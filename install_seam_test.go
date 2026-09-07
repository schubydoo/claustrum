package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestEnsureCLIWordsAHashFailureAsOpeningInput covers the D1 hash arm in
// ensureCLI's -cli-zst branch. It is unreachable without a seam: the branch has
// just opened the blob and read a byte from it, so any fixture that could make
// sha256File fail (missing, a directory, unreadable) has already been rejected
// two statements earlier with the same "opening input: " prefix.
//
// The assertion is on the WORDING, not on coverage. That arm exists so a hash
// failure keeps the prefix os.ReadFile once reported those conditions under; if
// it were dropped the empty blobSum would fall through to verifyChecksum and the
// caller would see "checksum mismatch: expected=…, actual=" — a different string
// on the __INSTALL_RESULT__ line for a condition that is not a mismatch.
func TestEnsureCLIWordsAHashFailureAsOpeningInput(t *testing.T) {
	errHash := errors.New("read blob: input/output error")
	old := hashBlobFile
	t.Cleanup(func() { hashBlobFile = old })
	hashBlobFile = func(string) (string, error) { return "", errHash }

	dir := t.TempDir()
	blob := filepath.Join(dir, "cli.zst")
	if err := os.WriteFile(blob, []byte("not really zstd"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A checksum is what activates D1's verification at all; without one the blob
	// is never hashed and the arm is not on the path.
	err := ensureCLI(installOpts{cliZst: blob, cliChecksum: "00"}, filepath.Join(dir, "cli"))
	want := fmt.Sprintf("opening input: %v", errHash)
	if err == nil || err.Error() != want {
		t.Fatalf("ensureCLI with a failing hash = %v, want %q", err, want)
	}
}

// TestFetchToFileRemovesTheTempWhenCloseFails covers fetchToFile's closeErr arm.
// A close that fails after every Write succeeded is a deferred write-back error
// (a full or failing filesystem reporting at close), which no portable fixture
// can stage — so the arm, and the os.Remove of the half-written blob it owes, is
// reachable only through the seam.
//
// Two assertions, because the arm does two things: it must surface THAT error
// (not the copy's, not a synthesized one) and it must not leave the temp behind
// for sweepFetchTemps to trip over.
func TestFetchToFileRemovesTheTempWhenCloseFails(t *testing.T) {
	errClose := errors.New("close: no space left on device")
	old := closeFetchTemp
	t.Cleanup(func() { closeFetchTemp = old })
	// Close the real descriptor before reporting the failure, so the fd is
	// released and fetchToFile's own os.Remove can delete the temp on Windows.
	closeFetchTemp = func(f *os.File) error { _ = f.Close(); return errClose }

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("payload"))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	path, sum, err := fetchToFile(srv.URL, dir)
	if !errors.Is(err, errClose) {
		t.Fatalf("fetchToFile with a failing close = %v, want %v", err, errClose)
	}
	if path != "" || sum != "" {
		t.Errorf("fetchToFile returned path=%q sum=%q on a close failure, want both empty", path, sum)
	}
	ents, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(ents) != 0 {
		t.Errorf("the download temp was left behind: %v", ents)
	}
}
