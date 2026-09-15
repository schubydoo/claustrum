package main

import (
	"encoding/json"
	"net"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestPluginsPruneLegacyGating covers the two legacy-root verdicts TestPluginsPruneSweepsRealRoot
// does not reach: a live sibling daemon skips the shared sweep entirely, and with no sibling an
// absent shared root reports "absent". The skip message is a wire string, so it is asserted whole.
// A mutant that swept the legacy root regardless of the sibling would delete the legacy dir here.
func TestPluginsPruneLegacyGating(t *testing.T) {
	const clientID, sibID = "c0ffee01", "c0ffee02"

	// newBase returns a short root (the AF_UNIX path under it must stay inside the macOS
	// sockaddr_un limit) with this daemon's run dir and listener already in place.
	newBase := func(t *testing.T) (base string, s *server) {
		t.Helper()
		base, err := os.MkdirTemp("", "cl")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(base) })
		runDir := filepath.Join(base, "run", clientID)
		if err := os.MkdirAll(runDir, 0o755); err != nil {
			t.Fatal(err)
		}
		ln, err := net.Listen("unix", filepath.Join(runDir, "rpc.sock"))
		if err != nil {
			t.Skipf("AF_UNIX listen unsupported here: %v", err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		return base, &server{ln: ln, procs: newTestProcManager(t)}
	}

	t.Run("a live sibling skips the shared sweep", func(t *testing.T) {
		base, s := newBase(t)
		sibRun := filepath.Join(base, "run", sibID)
		if err := os.MkdirAll(sibRun, 0o755); err != nil {
			t.Fatal(err)
		}
		sibLn, err := net.Listen("unix", filepath.Join(sibRun, "rpc.sock"))
		if err != nil {
			t.Skipf("AF_UNIX listen unsupported here: %v", err)
		}
		t.Cleanup(func() { _ = sibLn.Close() })

		// An old dir in the shared root that the sibling's sessions may still be using.
		legacyDir := filepath.Join(base, "plugins", "cccccccccccccccc")
		if err := os.MkdirAll(legacyDir, 0o755); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-60 * 24 * time.Hour)
		if err := os.Chtimes(legacyDir, old, old); err != nil {
			t.Fatal(err)
		}

		res := mustPrune(t, s)
		want := "skipped: another install's daemon is running (run/" + sibID +
			"/rpc.sock answers); its sessions may use the shared legacy dirs"
		if res.Legacy != want {
			t.Errorf("Legacy = %q, want %q", res.Legacy, want)
		}
		if len(res.PrunedLegacy) != 0 {
			t.Errorf("PrunedLegacy = %v, want none while a sibling is alive", res.PrunedLegacy)
		}
		if _, err := os.Stat(legacyDir); err != nil {
			t.Errorf("%s was swept while a sibling daemon is alive: %v", legacyDir, err)
		}
	})

	t.Run("no sibling and no shared root reports absent", func(t *testing.T) {
		_, s := newBase(t)
		if res := mustPrune(t, s); res.Legacy != "absent" {
			t.Errorf("Legacy = %q, want %q", res.Legacy, "absent")
		}
	})
}

// mustPrune dispatches one plugins.prune with the default params and returns its result.
func mustPrune(t *testing.T, s *server) pruneResult {
	t.Helper()
	resp := s.pluginsPrune(&request{ID: 1, Params: json.RawMessage(`{"minAgeDays":30}`)})
	if resp.Error != nil {
		t.Fatalf("unexpected error frame: %+v", resp.Error)
	}
	res, ok := resp.Result.(pruneResult)
	if !ok {
		t.Fatalf("result type = %T, want pruneResult", resp.Result)
	}
	return res
}

// siblingOwnID is the client id the sibling-probe tests pass as "our own", so the probe skips
// that entry and examines the other.
const siblingOwnID = "aaaaaaaa"

// mkSiblingRunDir builds <base>/run/{own,sib} and returns the base and the sibling's run dir.
// The root is a short os.MkdirTemp, not t.TempDir(): a t.TempDir path is built from the test
// name, and on macOS that already exceeds sockaddr_un's 104-byte sun_path, so every dial under
// it fails with EINVAL and the dial-classification arms can never be reached. The socket
// harness and TestSiblingDaemonAlive use os.MkdirTemp("", "cl") for the same reason.
func mkSiblingRunDir(t *testing.T, sib string) (base, sibRun string) {
	t.Helper()
	base, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	if err := os.MkdirAll(filepath.Join(base, "run", siblingOwnID), 0o755); err != nil {
		t.Fatal(err)
	}
	sibRun = filepath.Join(base, "run", sib)
	if err := os.MkdirAll(sibRun, 0o755); err != nil {
		t.Fatal(err)
	}
	return base, sibRun
}

// TestSiblingDaemonAliveDialArms covers the two per-socket verdicts that turn on the DIAL
// rather than the stat. Both run on every OS on purpose: the refused-dial arm is the only
// end-to-end exercise of the Windows classification (pluginsibling_windows.go's
// extraNobodyListening is otherwise only unit-tested in isolation), and sun_path is 108 bytes
// on Windows too, so the over-long path produces the same EINVAL there.
//
// Each verdict is a wire string embedded in plugins.prune's legacy field, so each is asserted
// whole or by prefix, never as "some non-empty answer".
func TestSiblingDaemonAliveDialArms(t *testing.T) {
	t.Run("a socket nobody listens on is not a sibling", func(t *testing.T) {
		// A plain file at rpc.sock: the stat succeeds and the connect fails in a way that
		// means nobody is listening, so the sweep proceeds. The errno differs per OS —
		// ECONNREFUSED on linux, ENOTSOCK on macOS, WSAECONNREFUSED on Windows — and
		// isNobodyListening accepts all three, which is the classification under test. A
		// mutant without it reports "cannot rule out" and keeps another install's plugins
		// forever.
		base, sibRun := mkSiblingRunDir(t, "bbbbbbbb")
		sock := filepath.Join(sibRun, "rpc.sock")
		if len(sock) > 100 {
			t.Skipf("temp root too long for sun_path (%d bytes): every dial here fails EINVAL, not refused", len(sock))
		}
		if err := os.WriteFile(sock, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := siblingDaemonAlive(base, siblingOwnID); got != "" {
			t.Errorf("refused dial: got %q, want \"\"", got)
		}
	})

	t.Run("an undialable socket is reported", func(t *testing.T) {
		// A path longer than sun_path (108 bytes on linux and Windows, 104 on macOS): the
		// file stats fine, but the connect fails with EINVAL, which does not mean nobody is
		// listening. A mutant that treated every dial failure as "gone" would sweep the
		// shared root while that daemon is alive. The length comes from the entry name, since
		// a directory name may be 255 bytes and sun_path may not.
		sib := strings.Repeat("d", 120)
		base, sibRun := mkSiblingRunDir(t, sib)
		sock := filepath.Join(sibRun, "rpc.sock")
		if err := os.WriteFile(sock, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if len(sock) <= 108 {
			t.Skipf("temp root too short to exceed sun_path: %d bytes", len(sock))
		}
		// The verdict embeds the forward-slash wire form on every OS (siblingDaemonAlive
		// builds it with path.Join), so this expectation is separator-clean as written.
		rel := path.Join("run", sib, "rpc.sock")
		got := siblingDaemonAlive(base, siblingOwnID)
		if !strings.HasPrefix(got, "cannot rule out "+rel+": ") {
			t.Errorf("undialable socket: got %q, want the cannot-rule-out verdict for %s", got, rel)
		}
	})
}

// TestSocketPathNoPathListener covers the two arms that report "no path-based listener".
// socketPath feeds pluginsLayoutFromSocket, so a mutant that returned a non-empty string for
// either would send the sweep at a root derived from a made-up path.
func TestSocketPathNoPathListener(t *testing.T) {
	if got := (&server{}).socketPath(); got != "" {
		t.Errorf("socketPath with no listener = %q, want empty", got)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listen unsupported here: %v", err)
	}
	defer ln.Close()
	if got := (&server{ln: ln}).socketPath(); got != "" {
		t.Errorf("socketPath on a tcp listener = %q, want empty", got)
	}
}

// TestLivePluginRefsNoProcManager pins the empty (not nil) map a daemon with no process
// manager returns: prunePluginRoot indexes the result, so a nil map would be a nil-map read
// on every candidate and the arm exists to keep that explicit.
func TestLivePluginRefsNoProcManager(t *testing.T) {
	refs := (&server{}).livePluginRefs()
	if refs == nil {
		t.Fatal("livePluginRefs returned a nil map")
	}
	if len(refs) != 0 {
		t.Errorf("livePluginRefs = %v, want empty", refs)
	}
}

// TestPrunePluginRootUnreadable covers the listing-failure arm: a plugin root that is not a
// directory is reported in Errors rather than silently swept. A missing root is NOT an error
// (the sweep runs before the CLI has ever written one), which is the arm's whole point, so
// both halves are asserted here.
func TestPrunePluginRootUnreadable(t *testing.T) {
	var res pruneResult
	missing := filepath.Join(t.TempDir(), "never-created")
	prunePluginRoot(missing, false, time.Now(), nil, &res)
	if len(res.Errors) != 0 {
		t.Errorf("a missing root reported errors: %v", res.Errors)
	}

	// On Windows a not-a-directory os.ReadDir error is ERROR_PATH_NOT_FOUND, which satisfies
	// os.IsNotExist, so a non-directory root reads as "absent" and no error is recorded —
	// the same OS split TestSiblingDaemonAliveRunDir documents for the run dir. The Unix
	// legs exercise the arm, where ENOTDIR is not fs.ErrNotExist.
	if runtime.GOOS == "windows" {
		return
	}
	notADir := filepath.Join(t.TempDir(), "plugins")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	prunePluginRoot(notADir, false, time.Now(), nil, &res)
	if len(res.Errors) != 1 {
		t.Fatalf("Errors = %v, want one entry for an unreadable root", res.Errors)
	}
	if !strings.HasPrefix(res.Errors[0], notADir+": ") {
		t.Errorf("Errors[0] = %q, want the root path and its error", res.Errors[0])
	}
}

// TestPruneOnePluginDirRemoveFails covers the delete-failure arm through the pluginRemoveAll
// seam. The directory must SURVIVE and be reported: a mutant that appended to Pruned anyway
// would report a hash the caller can still see on disk.
func TestPruneOnePluginDirRemoveFails(t *testing.T) {
	root := t.TempDir()
	const hash = "aaaaaaaaaaaaaaaa"
	dir := filepath.Join(root, hash)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-60 * 24 * time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}

	oldRm := pluginRemoveAll
	t.Cleanup(func() { pluginRemoveAll = oldRm })
	pluginRemoveAll = func(string) error { return syscall.EACCES }

	var res pruneResult
	prunePluginRoot(root, false, time.Now().Add(-30*24*time.Hour), nil, &res)

	if len(res.Pruned) != 0 {
		t.Errorf("Pruned = %v, want none — the removal failed", res.Pruned)
	}
	if len(res.Errors) != 1 {
		t.Fatalf("Errors = %v, want one entry for the failed removal", res.Errors)
	}
	if !strings.HasPrefix(res.Errors[0], dir+": ") {
		t.Errorf("Errors[0] = %q, want the directory path and its error", res.Errors[0])
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("%s should still exist after a failed removal: %v", dir, err)
	}
}
