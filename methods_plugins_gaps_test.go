package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
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
