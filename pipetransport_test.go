package main

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOwnerOnlySDDL asserts the pipe's security descriptor is the exact owner-only
// DACL the AF_UNIX socket's 0600 mode is analogous to: a protected DACL granting
// GENERIC_ALL to the daemon user's SID and to NO world principal. This is the
// "owner" half of the guardrail (a foreign SID cannot open the pipe); spinning up
// a real foreign-SID client in CI isn't feasible, so the construction is asserted
// here and the token half is exercised over real pipe I/O in the Windows test.
func TestOwnerOnlySDDL(t *testing.T) {
	const sid = "S-1-5-21-1111111111-2222222222-3333333333-1001"
	got := ownerOnlySDDL(sid)
	if want := "D:P(A;;GA;;;" + sid + ")"; got != want {
		t.Fatalf("ownerOnlySDDL = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, "D:P") {
		t.Errorf("DACL is not protected (missing D:P): %q", got)
	}
	// No ACE for a world principal: Everyone (S-1-1-0), Authenticated Users
	// (S-1-5-11), anonymous (S-1-5-7), or a NULL DACL (D:NO_ACCESS_CONTROL / "D:\n").
	for _, world := range []string{"S-1-1-0", "S-1-5-11", "S-1-5-7", "NO_ACCESS_CONTROL"} {
		if strings.Contains(got, world) {
			t.Errorf("SDDL grants a world principal %q: %q", world, got)
		}
	}
}

func TestPipePath(t *testing.T) {
	if got, want := pipePath("deadbeef"), `\\.\pipe\claustrum-deadbeef`; got != want {
		t.Fatalf("pipePath = %q, want %q", got, want)
	}
}

func TestNewInstanceID(t *testing.T) {
	a, err := newInstanceID()
	if err != nil {
		t.Fatalf("newInstanceID: %v", err)
	}
	if len(a) != 16 { // 8 random bytes → 16 hex chars
		t.Fatalf("instance id %q has len %d, want 16", a, len(a))
	}
	if _, err := hex.DecodeString(a); err != nil {
		t.Fatalf("instance id %q is not hex: %v", a, err)
	}
	b, err := newInstanceID()
	if err != nil {
		t.Fatalf("newInstanceID (2nd): %v", err)
	}
	if a == b {
		t.Fatalf("two instance ids collided: %q", a)
	}
}

// TestPipeNameFileRoundTrip covers the rpc.pipe lifecycle: atomic publish beside
// the socket, overwrite of a stale value, no leftover temp files, and a
// remove-then-remove-again that is a silent no-op the second time.
func TestPipeNameFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "rpc.sock")
	const name = `\\.\pipe\claustrum-abc123`

	if err := writePipeNameFile(sock, name); err != nil {
		t.Fatalf("writePipeNameFile: %v", err)
	}
	path := filepath.Join(dir, pipeNameFileName)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rpc.pipe: %v", err)
	}
	if string(got) != name {
		t.Fatalf("rpc.pipe = %q, want %q", got, name)
	}

	// Overwrite with a fresh name (a re-run in the same dir must win cleanly).
	const name2 = `\\.\pipe\claustrum-def456`
	if err := writePipeNameFile(sock, name2); err != nil {
		t.Fatalf("writePipeNameFile (overwrite): %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != name2 {
		t.Fatalf("rpc.pipe after overwrite = %q, want %q", got, name2)
	}

	assertNoPipeTemps(t, dir)

	removePipeNameFile(sock)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("rpc.pipe still present after remove: err=%v", err)
	}
	// Absent file → silent no-op (must not panic or error).
	removePipeNameFile(sock)
}

// TestWritePipeNameFileCreateError: a socket whose directory does not exist makes
// os.CreateTemp fail, and writePipeNameFile surfaces the error instead of panicking.
func TestWritePipeNameFileCreateError(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "nope", "rpc.sock") // parent "nope" absent
	if err := writePipeNameFile(sock, "x"); err == nil {
		t.Fatal("writePipeNameFile into a missing dir should error")
	}
	// And nothing was left behind in the (existing) grandparent dir.
	assertNoPipeTemps(t, filepath.Dir(filepath.Dir(sock)))
}

// TestRemovePipeNameFileStatError: when the path's parent is a regular file, Stat
// fails with a non-IsNotExist error (ENOTDIR); removePipeNameFile logs and returns
// without panicking. Mirrors the daemon.token StatError case.
func TestRemovePipeNameFileStatError(t *testing.T) {
	dir := t.TempDir()
	notDir := filepath.Join(dir, "file")
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	// socket "under" a regular file → filepath.Dir is the file → Stat(file/rpc.pipe)
	// returns ENOTDIR, exercising the non-IsNotExist branch.
	removePipeNameFile(filepath.Join(notDir, "rpc.sock"))
}

// TestWritePipeNameFileWriteAndCloseErrors drives the best-effort error arms via
// the createPipeTemp seam (a real *os.File can't be coerced into failing
// WriteString/Close), and asserts each arm cleans up its temp and surfaces the
// error. Reuses fakeTokenTemp from tokenpersist_test.go.
func TestWritePipeNameFileWriteAndCloseErrors(t *testing.T) {
	for _, tc := range []struct {
		name               string
		writeErr, closeErr error
	}{
		{"write-fails", errInjected, nil},
		{"close-fails", nil, errInjected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			sock := filepath.Join(dir, "rpc.sock")
			real, err := os.CreateTemp(dir, "rpc.pipe-*")
			if err != nil {
				t.Fatal(err)
			}
			_ = real.Close()
			restore := createPipeTemp
			createPipeTemp = func(string) (tokenTempFile, error) {
				return &fakeTokenTemp{name: real.Name(), writeErr: tc.writeErr, closeErr: tc.closeErr}, nil
			}
			t.Cleanup(func() { createPipeTemp = restore })

			if err := writePipeNameFile(sock, "x"); err == nil {
				t.Fatal("writePipeNameFile should surface the injected error")
			}
			if _, err := os.Stat(pipeNameFilePath(sock)); !os.IsNotExist(err) {
				t.Fatalf("rpc.pipe should not exist after a failed write: err=%v", err)
			}
			assertNoPipeTemps(t, dir)
		})
	}
}

// TestWritePipeNameFileRenameError: when the target path is already a directory,
// os.Rename fails and writePipeNameFile cleans up its temp and returns the error.
func TestWritePipeNameFileRenameError(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "rpc.sock")
	if err := os.Mkdir(pipeNameFilePath(sock), 0o755); err != nil { // rpc.pipe is a dir
		t.Fatalf("mkdir: %v", err)
	}
	if err := writePipeNameFile(sock, "x"); err == nil {
		t.Fatal("writePipeNameFile should fail to rename over a directory")
	}
	assertNoPipeTemps(t, dir)
}

// TestRemovePipeNameFileRemoveError: when rpc.pipe is a NON-EMPTY directory, Stat
// succeeds but os.Remove fails with ENOTEMPTY, exercising the remove-failure log
// arm (must not panic). Mirrors the daemon.token RemoveFailure case.
func TestRemovePipeNameFileRemoveError(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "rpc.sock")
	pipeDir := pipeNameFilePath(sock)
	if err := os.Mkdir(pipeDir, 0o755); err != nil {
		t.Fatalf("mkdir rpc.pipe dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pipeDir, "child"), []byte("x"), 0o600); err != nil {
		t.Fatalf("seed child: %v", err)
	}
	removePipeNameFile(sock) // Remove fails (non-empty dir); logs and returns.
}

// TestEnablePipeClearsStaleFileWhenDisabled: a leftover rpc.pipe from a prior
// unclean crash names a per-boot-random pipe that no longer exists. When this boot
// does not serve a pipe (flag off — the case on every non-Windows platform, and on
// Windows without -listen-pipe), enablePipe must remove that stale file so a client
// can never read it and dial a dead pipe. Mirrors the stale-socket clear.
func TestEnablePipeClearsStaleFileWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "rpc.sock")
	stale := pipeNameFilePath(sock)
	if err := os.WriteFile(stale, []byte(`\\.\pipe\claustrum-dead`), 0o600); err != nil {
		t.Fatalf("seed stale rpc.pipe: %v", err)
	}
	s := &server{}
	s.enablePipe(sock, false)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale rpc.pipe not removed when pipe disabled: err=%v", err)
	}
	if s.pipeLn != nil {
		t.Fatal("enablePipe(false) must not set pipeLn")
	}
}

// assertNoPipeTemps fails if any half-written rpc.pipe-* temp survived (the atomic
// write must always rename or clean up).
func assertNoPipeTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "rpc.pipe-") {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
	}
}
