package main

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"
)

// TestSiblingDaemonAlive proves the legacy-sweep guard: a run/<sib>/rpc.sock that a
// live listener answers is reported by its relative path; a run dir with no
// answering socket is not. A mutant that skipped the dial (always "" or always the
// first sibling) would fail one of the two arms.
func TestSiblingDaemonAlive(t *testing.T) {
	// A short temp root, not t.TempDir(): the full AF_UNIX socket path must stay
	// under the macOS sockaddr_un limit (~104 bytes), the same reason the socket
	// harness uses os.MkdirTemp("", "cl").
	base, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	const own, sib = "aaaaaaaa", "bbbbbbbb"
	mkRun := func(id string) string {
		d := filepath.Join(base, "run", id)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		return filepath.Join(d, "rpc.sock")
	}
	mkRun(own)
	sock := mkRun(sib)
	if got := siblingDaemonAlive(base, own); got != "" {
		t.Errorf("no sibling listener: got %q, want \"\"", got)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("AF_UNIX listen unsupported here: %v", err)
	}
	// The return value is a wire string (embedded in the skip message), so it uses
	// forward slashes on every OS — path.Join, matching siblingDaemonAlive. A
	// filepath.Join here would expect "run\\...\\rpc.sock" on Windows and mismatch.
	want := path.Join("run", sib, "rpc.sock")
	if got := siblingDaemonAlive(base, own); got != want {
		t.Errorf("live sibling: got %q, want %q", got, want)
	}
	_ = ln.Close()
	_ = os.Remove(sock)
	if got := siblingDaemonAlive(base, own); got != "" {
		t.Errorf("dead sibling: got %q, want \"\"", got)
	}
	// own's own socket is skipped even when it has a live listener.
	if ownLn, oerr := net.Listen("unix", mkRun(own)); oerr == nil {
		defer ownLn.Close()
		if got := siblingDaemonAlive(base, own); got != "" {
			t.Errorf("own listener must be skipped: got %q, want \"\"", got)
		}
	}
}

func TestPluginsLayoutFromSocket(t *testing.T) {
	cases := []struct {
		sock, root, legacy string
		ok                 bool
	}{
		{"/x/run/c0ffee01/rpc.sock", "/x/plugins/c0ffee01", "/x/plugins", true},
		{"/home/u/.claude/remote/run/abcd1234/rpc.sock", "/home/u/.claude/remote/plugins/abcd1234", "/home/u/.claude/remote/plugins", true},
		{"/tmp/cssh-probe.X/rpc.sock", "", "", false},  // not under run/<clientId>
		{"/x/run/c0ffee01/other.sock", "", "", false},  // wrong socket basename
		{"/x/notrun/c0ffee01/rpc.sock", "", "", false}, // parent-of-parent not "run"
		{"", "", "", false}, // no path (abstract socket / pipe)
	}
	for _, c := range cases {
		// The roots are built with filepath.Join, so the expected values use native
		// separators (a no-op on Unix, "\\" on Windows). FromSlash("") stays "".
		wantRoot, wantLegacy := filepath.FromSlash(c.root), filepath.FromSlash(c.legacy)
		root, legacy, ok := pluginsLayoutFromSocket(c.sock)
		if ok != c.ok || root != wantRoot || legacy != wantLegacy {
			t.Errorf("pluginsLayoutFromSocket(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.sock, root, legacy, ok, wantRoot, wantLegacy, c.ok)
		}
	}
}

// TestPrunePluginRootClassification proves the per-entry classification measured
// against 19f30c46: a plugin hash dir is kept when live (its hash is in a running
// child's argv) or young, and removed otherwise. "last used" is max(dir mtime,
// .synced mtime) — a fresh .synced keeps an old dir, a missing .synced falls back
// to the dir mtime. A non-hash dir and any plain file are left untouched (keep does
// not protect, and there is no archive sweep). A mutant using the dir mtime alone
// would misclassify the .synced-young dir, and a mutant sweeping files would delete
// the plain file.
func TestPrunePluginRootClassification(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-60 * 24 * time.Hour)
	young := time.Now().Add(-1 * 24 * time.Hour)
	mkPlugin := func(name string, dirMT, syncedMT time.Time, withSynced bool) {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Join(p, ".claude-plugin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "manifest.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if withSynced {
			s := filepath.Join(p, ".synced")
			if err := os.WriteFile(s, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(s, syncedMT, syncedMT); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chtimes(p, dirMT, dirMT); err != nil {
			t.Fatal(err)
		}
	}
	const (
		pruneHash = "aaaaaaaaaaaaaaaa" // dir old, .synced old -> pruned
		dirYoung  = "bbbbbbbbbbbbbbbb" // dir young, .synced old -> young
		syncYoung = "cccccccccccccccc" // dir old, .synced young -> young (max wins)
		liveHash  = "dddddddddddddddd" // dir old, .synced old, but live -> live
		noSynced  = "eeeeeeeeeeeeeeee" // dir old, no .synced -> pruned (dir mtime)
	)
	mkPlugin(pruneHash, old, old, true)
	mkPlugin(dirYoung, young, old, true)
	mkPlugin(syncYoung, old, young, true)
	mkPlugin(liveHash, old, old, true)
	mkPlugin(noSynced, old, old, false)
	if err := os.Mkdir(filepath.Join(root, "not-a-plugin-hash"), 0o755); err != nil { // non-hex name -> ignored
		t.Fatal(err)
	}
	stale := filepath.Join(root, "stale.tar.zst") // a plain file -> left untouched
	if err := os.WriteFile(stale, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	res := pruneResult{Pruned: []string{}, PrunedLegacy: []string{}}
	prunePluginRoot(root, false, cutoff, map[string]bool{liveHash: true}, &res)

	sort.Strings(res.Pruned)
	if want := []string{pruneHash, noSynced}; !slices.Equal(res.Pruned, want) {
		t.Errorf("Pruned = %v, want %v", res.Pruned, want)
	}
	if res.Live != 1 || res.Young != 2 || res.Kept != 0 || res.StaleArchives != 0 {
		t.Errorf("counts live=%d young=%d kept=%d stale=%d, want live=1 young=2 kept=0 stale=0",
			res.Live, res.Young, res.Kept, res.StaleArchives)
	}
	for _, keptName := range []string{dirYoung, syncYoung, liveHash, "not-a-plugin-hash", "stale.tar.zst"} {
		if _, err := os.Stat(filepath.Join(root, keptName)); err != nil {
			t.Errorf("%s should survive: %v", keptName, err)
		}
	}
	for _, gone := range []string{pruneHash, noSynced} {
		if _, err := os.Stat(filepath.Join(root, gone)); !os.IsNotExist(err) {
			t.Errorf("%s should be removed", gone)
		}
	}
}

// TestSocketPluginsPrune pins the wire frames the in-repo harness can reach: the
// skip result (the harness socket is a /tmp socket, not run/<clientId>/rpc.sock),
// the -32602 detail (both the field-mismatch and the value-mismatch that embeds
// handlers.PruneParams), and the -32601 for an unknown method in the namespace.
func TestSocketPluginsPrune(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	got := []json.RawMessage{
		cl.call(req(1, "plugins.prune", map[string]any{})),
		cl.call(req(2, "plugins.prune", map[string]any{"minAgeDays": "x"})),
		cl.call(authed(`{"jsonrpc":"2.0","id":3,"method":"plugins.prune","params":[]}`)),
		cl.call(req(4, "plugins.bogus", map[string]any{})),
	}
	assertGolden(t, "socket_plugins_prune.golden.json", encodeGolden(t, got))
}

// TestPluginsPruneSweepsRealRoot drives the real sweep path that the /tmp-socket
// harness cannot reach: a daemon whose socket is shaped run/<clientId>/rpc.sock,
// so both the per-install and the shared legacy roots are swept. It seeds four
// plugin hash dirs — one old (pruned), one young (kept), one live (kept because
// its hash appears in a running child's argv even though its mtime is old), and
// one old dir in the legacy root (prunedLegacy) — and asserts the classification.
// A mutant that ignored the live set prunes the live dir; a mutant that pruned
// nothing (treated every dir as young) leaves the old dir; a mutant that skipped the
// legacy sweep leaves the legacy dir. No sibling daemon runs, so legacy is "swept".
// This test seeds no .synced marker, so it does not exercise the max(dir, .synced)
// last-used rule — TestPrunePluginRootClassification covers that.
func TestPluginsPruneSweepsRealRoot(t *testing.T) {
	// A short temp root, not t.TempDir(): the AF_UNIX socket path lives under it and
	// must stay under the macOS sockaddr_un limit (~104 bytes).
	base, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })

	const clientID = "c0ffee01"
	runDir := filepath.Join(base, "run", clientID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(runDir, "rpc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("AF_UNIX listen unsupported here: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	perInstall := filepath.Join(base, "plugins", clientID)
	legacyRoot := filepath.Join(base, "plugins")
	old := time.Now().Add(-60 * 24 * time.Hour)
	young := time.Now().Add(-1 * 24 * time.Hour)
	mk := func(root, name string, mt time.Time) {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	const (
		liveHash  = "dddddddddddddddd" // old mtime but live -> kept
		oldHash   = "aaaaaaaaaaaaaaaa" // old -> pruned
		youngHash = "bbbbbbbbbbbbbbbb" // young -> kept
		legacyOld = "cccccccccccccccc" // old, in legacy root -> prunedLegacy
	)
	mk(perInstall, liveHash, old)
	mk(perInstall, oldHash, old)
	mk(perInstall, youngHash, young)
	mk(legacyRoot, legacyOld, old)

	s := &server{ln: ln, procs: newTestProcManager(t)}
	// Inject a running child whose argv references the live plugin dir. liveArgv
	// reads only p.running and p.cmd.Args, so a never-started exec.Cmd is enough and
	// is deterministic (no real process to race or reap). The command name carries
	// no 16-hex run, so the live hash is the only one livePluginRefs can find.
	s.procs.mu.Lock()
	s.procs.procs["live"] = &managedProc{
		id:      "live",
		running: true,
		cmd:     exec.Command("claude-cli", "--plugin-dir", filepath.Join(perInstall, liveHash)),
	}
	s.procs.mu.Unlock()

	resp := s.pluginsPrune(&request{ID: 1, Params: json.RawMessage(`{"minAgeDays":30}`)})
	if resp.Error != nil {
		t.Fatalf("unexpected error frame: %+v", resp.Error)
	}
	res, ok := resp.Result.(pruneResult)
	if !ok {
		t.Fatalf("result type = %T, want pruneResult", resp.Result)
	}
	if res.Root != perInstall {
		t.Errorf("Root = %q, want %q", res.Root, perInstall)
	}
	if res.MinAgeDays != 30 {
		t.Errorf("MinAgeDays = %d, want 30", res.MinAgeDays)
	}
	if res.Legacy != "swept" {
		t.Errorf("Legacy = %q, want %q", res.Legacy, "swept")
	}
	if res.Live != 1 || res.Young != 1 || res.Kept != 0 || res.StaleArchives != 0 {
		t.Errorf("counts live=%d young=%d kept=%d stale=%d, want live=1 young=1 kept=0 stale=0",
			res.Live, res.Young, res.Kept, res.StaleArchives)
	}
	if !slices.Equal(res.Pruned, []string{oldHash}) {
		t.Errorf("Pruned = %v, want %v", res.Pruned, []string{oldHash})
	}
	if !slices.Equal(res.PrunedLegacy, []string{legacyOld}) {
		t.Errorf("PrunedLegacy = %v, want %v", res.PrunedLegacy, []string{legacyOld})
	}
	for _, survivor := range []string{filepath.Join(perInstall, liveHash), filepath.Join(perInstall, youngHash)} {
		if _, err := os.Stat(survivor); err != nil {
			t.Errorf("%s should survive: %v", survivor, err)
		}
	}
	for _, gone := range []string{filepath.Join(perInstall, oldHash), filepath.Join(legacyRoot, legacyOld)} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s should be removed", gone)
		}
	}
}
