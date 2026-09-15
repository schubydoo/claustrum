//go:build unix

package main

import (
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// The sibling probe's per-socket arms, which need specific errno shapes and so are Unix-only
// (TestSiblingDaemonAliveRunDir documents the same split for the run-dir arms). Each verdict
// is a wire string embedded in plugins.prune's legacy field, so each is asserted whole or by
// prefix, never as "some non-empty answer".
func TestSiblingDaemonAliveSocketArms(t *testing.T) {
	const own = "aaaaaaaa"

	// mkSib returns the base and the sibling's run dir for one case. The root is a short
	// os.MkdirTemp, not t.TempDir(): a macOS t.TempDir path is built from the test name and
	// already exceeds sockaddr_un's 104-byte sun_path, so every dial under it fails with
	// EINVAL and the refused-connection case can never arise. The same reason the socket
	// harness and TestSiblingDaemonAlive use os.MkdirTemp("", "cl").
	mkSib := func(t *testing.T, sib string) (base, sibRun string) {
		t.Helper()
		base, err := os.MkdirTemp("", "cl")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(base) })
		if err := os.MkdirAll(filepath.Join(base, "run", own), 0o755); err != nil {
			t.Fatal(err)
		}
		sibRun = filepath.Join(base, "run", sib)
		if err := os.MkdirAll(sibRun, 0o755); err != nil {
			t.Fatal(err)
		}
		return base, sibRun
	}

	t.Run("a socket nobody listens on is not a sibling", func(t *testing.T) {
		// A plain file at rpc.sock: the stat succeeds, and connect answers ECONNREFUSED —
		// nobody is listening, so the sweep proceeds. A mutant without that classification
		// reports "cannot rule out" and keeps another install's plugins forever.
		base, sibRun := mkSib(t, "bbbbbbbb")
		sock := filepath.Join(sibRun, "rpc.sock")
		if len(sock) > 100 {
			t.Skipf("temp root too long for sun_path (%d bytes): every dial here fails EINVAL, not ECONNREFUSED", len(sock))
		}
		if err := os.WriteFile(sock, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := siblingDaemonAlive(base, own); got != "" {
			t.Errorf("refused dial: got %q, want \"\"", got)
		}
	})

	t.Run("an unstattable socket is reported", func(t *testing.T) {
		// A self-referential symlink: the stat fails with ELOOP, which is neither
		// not-found nor not-a-directory, so the sibling cannot be ruled out.
		base, sibRun := mkSib(t, "cccccccc")
		sock := filepath.Join(sibRun, "rpc.sock")
		if err := os.Symlink(sock, sock); err != nil {
			t.Fatal(err)
		}
		got := siblingDaemonAlive(base, own)
		if !strings.HasPrefix(got, "cannot inspect "+sock+": ") {
			t.Errorf("unstattable socket: got %q, want the cannot-inspect verdict for %s", got, sock)
		}
	})

	t.Run("an undialable socket is reported", func(t *testing.T) {
		// A path longer than sockaddr_un's sun_path (108 bytes on linux, 104 on darwin):
		// the file stats fine, but the connect fails with EINVAL, which does not mean
		// nobody is listening. A mutant that treated every dial failure as "gone" would
		// sweep the shared root while that daemon is alive.
		// The probe looks for run/<entry>/rpc.sock, so the length comes from the entry
		// name (a directory name may be 255 bytes; sun_path may not).
		sib := strings.Repeat("d", 120)
		base, sibRun := mkSib(t, sib)
		sock := filepath.Join(sibRun, "rpc.sock")
		if err := os.WriteFile(sock, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if len(sock) <= 108 {
			t.Skipf("temp root too short to exceed sun_path: %d bytes", len(sock))
		}
		rel := path.Join("run", sib, "rpc.sock")
		got := siblingDaemonAlive(base, own)
		if !strings.HasPrefix(got, "cannot rule out "+rel+": ") {
			t.Errorf("undialable socket: got %q, want the cannot-rule-out verdict for %s", got, rel)
		}
	})
}
