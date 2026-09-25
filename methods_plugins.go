package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/schubydoo/claustrum/handlers"
)

// plugins.prune (reference build 19f30c46) prunes cached CLI plugin directories
// that no active session references and that have not been used within minAgeDays.
// A plugin dir is named by its content hash (16 lowercase hex). Two roots are
// swept: the per-install root plugins/<clientId>/ (results in `pruned`) and the
// shared legacy root plugins/ (results in `prunedLegacy`), both relative to the
// daemon socket's run/<clientId>/rpc.sock layout. The per-install sweep always
// runs. The legacy sweep is skipped while any sibling daemon on the host is present
// (it answers, or its liveness cannot be ruled out), since its sessions can
// reference the shared legacy plugins.
//
// "last used" is the newer of the dir's own mtime and its .synced marker's mtime
// (the manifest.json mtime does not count). A dir is kept when it is live (its hash
// appears in a running child's argv — the CLI is launched with --plugin-dir
// <...>/<hash>) or young (last-used within minAgeDays); otherwise it is removed and
// its hash is listed (sorted). The caller's params.keep list does NOT protect a dir
// and no plain file is swept, so `kept` and `staleArchives` stay 0 (accepted as
// parity fields; their reference triggers are not pinned). Measured against
// 19f30c46 on the VM and against a live Desktop session.

// pluginHashRE matches a plugin content-hash directory name (16 lowercase hex).
var pluginHashRE = regexp.MustCompile(`^[0-9a-f]{16}$`)

// pruneResult is plugins.prune's reply. Field order is the reference's, so the
// serialized frame is byte-compatible. pruned/prunedLegacy are never omitempty
// (they emit [] when empty); skipped/errors are omitempty.
type pruneResult struct {
	Root          string   `json:"root"`
	Pruned        []string `json:"pruned"`
	PrunedLegacy  []string `json:"prunedLegacy"`
	StaleArchives int      `json:"staleArchives"`
	Kept          int      `json:"kept"`
	Live          int      `json:"live"`
	Young         int      `json:"young"`
	Legacy        string   `json:"legacy"`
	Skipped       string   `json:"skipped,omitempty"`
	Errors        []string `json:"errors,omitempty"`
	MinAgeDays    int      `json:"minAgeDays"`
}

func (s *server) handlePlugins(req *request) response {
	switch req.Method {
	case "plugins.prune":
		return s.pluginsPrune(req)
	default:
		// A well-formed method in the plugins namespace that does not exist answers
		// "Unknown method: plugins.<x>" (the namespace is known), distinct from the
		// "Unknown namespace: plugins" a build without this handler returns.
		return unknownMethod(req)
	}
}

const pluginsSkipReason = "this daemon's socket is not under run/<clientId>/, so it has no per-install plugin root to prune"

func (s *server) pluginsPrune(req *request) response {
	// plugins.prune decodes its own params so the -32602 body carries encoding/json's
	// detail ("Invalid params: json: cannot unmarshal ..."), matching the reference —
	// which is why PruneParams lives in package handlers (the type name reaches the
	// wire). This differs from the shared bindParams path, whose bare "Invalid params"
	// the other 18 methods use.
	var p handlers.PruneParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResult(req.ID, codeInvalidParam, "Invalid params: "+err.Error())
		}
	}

	res := pruneResult{Pruned: []string{}, PrunedLegacy: []string{}}
	root, legacyRoot, ok := pluginsLayoutFromSocket(s.socketPath())
	if !ok {
		// No per-install plugin root: the socket is not shaped run/<clientId>/rpc.sock.
		// root stays "" and minAgeDays stays 0 — the clamp below never runs.
		res.Legacy = "skipped: " + pluginsSkipReason
		res.Skipped = pluginsSkipReason
		return okResult(req.ID, res)
	}
	res.Root = root

	// Clamp the retention window to [7, 3650] days. An absent minAgeDays is 0, which
	// clamps up to 7. The clamped value is echoed in the reply.
	minAge := min(max(p.MinAgeDays, 7), 3650)
	res.MinAgeDays = minAge
	cutoff := time.Now().Add(-time.Duration(minAge) * 24 * time.Hour)

	live := s.livePluginRefs()

	// Per-install root -> pruned[]. Always swept (it is this client's own), whether
	// or not a sibling daemon is alive. An absent root is not an error, just empty.
	prunePluginRoot(root, false, cutoff, live, &res)
	// Shared legacy root -> prunedLegacy[]. The sibling check gates the legacy sweep
	// and runs FIRST: when another install's daemon on this host may be present, its
	// sessions may reference the shared legacy plugins, so the legacy field is the
	// skip reason naming the first such sibling — even when the shared root does not
	// exist (measured against 19f30c46). With no sibling present, the shared root is
	// "absent" when missing, or "swept" and classified like the per-install root when
	// it exists. The reason
	// detail comes from siblingDaemonAlive: a socket that answers, or one that cannot
	// be ruled out gone.
	base := filepath.Dir(legacyRoot) // <X>: the parent of run/ and plugins/
	if sib := siblingDaemonAlive(base, filepath.Base(root)); sib != "" {
		res.Legacy = "skipped: another install's daemon is running (" + sib + "); its sessions may use the shared legacy dirs"
	} else if _, err := os.Stat(legacyRoot); err != nil {
		res.Legacy = "absent"
	} else {
		res.Legacy = "swept"
		prunePluginRoot(legacyRoot, true, cutoff, live, &res)
	}

	sort.Strings(res.Pruned)
	sort.Strings(res.PrunedLegacy)
	return okResult(req.ID, res)
}

// pluginsLayoutFromSocket derives the plugin roots from the daemon socket path.
// The socket is <X>/run/<clientId>/rpc.sock; the per-install root is
// <X>/plugins/<clientId> and the shared legacy root is <X>/plugins. ok is false
// (a skip) when the socket is not that shape.
func pluginsLayoutFromSocket(sock string) (root, legacyRoot string, ok bool) {
	if sock == "" || filepath.Base(sock) != "rpc.sock" {
		return "", "", false
	}
	clientDir := filepath.Dir(sock)      // <X>/run/<clientId>
	clientID := filepath.Base(clientDir) // <clientId>
	runDir := filepath.Dir(clientDir)    // <X>/run
	if filepath.Base(runDir) != "run" || clientID == "" || clientID == "." {
		return "", "", false
	}
	base := filepath.Dir(runDir) // <X>
	return filepath.Join(base, "plugins", clientID), filepath.Join(base, "plugins"), true
}

// siblingDaemonAlive reports whether another install's daemon on this host may be
// present, in which case its sessions may reference the shared legacy plugins and
// the caller skips the legacy sweep. It returns the detail to embed in the skip
// message for the FIRST sibling that may be present, or "" when every sibling is
// provably gone (so the sweep runs). run/<clientId>/ dirs under base other than own
// are examined in os.ReadDir (sorted) order. claustrum stats each socket, THEN
// dials it, and only a genuinely absent or dead socket clears it. A
// permission-denied stat, a slow (timed-out) dial, or any dial error other than
// connection-refused / not-a-socket / not-found leaves the sibling's liveness in
// doubt and spares the shared legacy plugins. Treating every dial error as "gone"
// was the divergence: claustrum swept another install's legacy plugins where the
// reference kept them.
//
// The forms of the returned detail (the caller wraps each into "skipped: another
// install's daemon is running (<detail>); its sessions may use the shared legacy dirs"):
//
//	"<rel> answers"                 the socket accepted a connection
//	"cannot rule out <rel>: <err>"  the dial failed for a non-nobody-listening reason
//	"cannot inspect <sock>: <err>"  the stat failed other than not-found / not-a-dir
//	"cannot read <runDir>: <err>"   the run dir itself could not be listed
//
// <rel> is the forward-slash wire path "run/<clientId>/rpc.sock"; "cannot inspect"
// names the absolute socket path and "cannot read" the run dir, matching the reference.
func siblingDaemonAlive(base, own string) string {
	runDir := filepath.Join(base, "run")
	entries, err := os.ReadDir(runDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ""
		}
		return "cannot read " + runDir + ": " + err.Error()
	}
	for _, e := range entries {
		if e.Name() == own {
			continue
		}
		sock := filepath.Join(runDir, e.Name(), "rpc.sock")
		// The relative form is a wire string embedded in the skip message, so it uses
		// forward slashes on every OS (path.Join, not filepath.Join) — matching the
		// reference's "run/<clientId>/rpc.sock".
		rel := path.Join("run", e.Name(), "rpc.sock")
		if _, serr := os.Stat(sock); serr != nil {
			if errors.Is(serr, fs.ErrNotExist) || errors.Is(serr, syscall.ENOTDIR) {
				continue
			}
			return "cannot inspect " + sock + ": " + serr.Error()
		}
		c, derr := net.DialTimeout("unix", sock, 300*time.Millisecond)
		if derr == nil {
			_ = c.Close()
			return rel + " answers"
		}
		if isNobodyListening(derr) {
			continue
		}
		return "cannot rule out " + rel + ": " + derr.Error()
	}
	return ""
}

// isNobodyListening reports whether a sibling dial error means the socket has no
// live listener — connection refused, the path is not a socket, or the socket file
// is gone — so the sibling counts as absent and does not gate the legacy sweep.
// Any other error (permission denied, timeout, connection reset) leaves the sibling
// possibly present, so the caller keeps the shared legacy plugins.
// extraNobodyListening adds the Windows-only Winsock refused error
// (pluginsibling_windows.go), because a refused dial there is WSAECONNREFUSED, not
// syscall.ECONNREFUSED.
func isNobodyListening(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ENOTSOCK) ||
		errors.Is(err, syscall.ENOENT) ||
		errors.Is(err, fs.ErrNotExist) ||
		extraNobodyListening(err)
}

// socketPath returns the filesystem path of the daemon's AF_UNIX listener, or ""
// when there is no path-based listener (an abstract socket, a pipe, or no listener).
func (s *server) socketPath() string {
	if s.ln == nil {
		return ""
	}
	addr := s.ln.Addr()
	if addr == nil || addr.Network() != "unix" {
		return ""
	}
	return addr.String()
}

// livePluginRefs collects the plugin hashes referenced by any running child's argv
// (the CLI is spawned with --plugin-dir <...>/<hash>): each argv is split into
// maximal hex runs, and the 16-hex runs are the candidate hashes. This covers THIS
// daemon's own children. A sibling daemon's live plugins are handled coarsely at
// the legacy root instead: siblingDaemonAlive skips the whole legacy sweep while
// any sibling is present (it answers, or its liveness cannot be ruled out), matching
// the reference.
func (s *server) livePluginRefs() map[string]bool {
	set := map[string]bool{}
	if s.procs == nil {
		return set
	}
	for _, argv := range s.procs.liveArgv() {
		for _, arg := range argv {
			for _, tok := range strings.FieldsFunc(arg, notHexDigit) {
				if len(tok) == 16 {
					set[tok] = true
				}
			}
		}
	}
	return set
}

func notHexDigit(r rune) bool {
	return (r < '0' || r > '9') && (r < 'a' || r > 'f')
}

// prunePluginRoot sweeps one plugin root. A directory named by a plugin content
// hash is kept when it is live (its hash appears in a running child's argv) or
// young (its last-used time is within minAgeDays); otherwise it is removed and its
// hash appended to pruned (per-install root) or prunedLegacy (shared root), sorted
// by the caller. Non-directory entries are left untouched — measured against
// 19f30c46, which swept no plain file regardless of age or extension.
//
// The caller's params.keep list does NOT protect a plugin: the reference pruned a
// valid old plugin whose bare hash was in keep (kept stayed 0). The Kept counter is
// therefore never populated here, and keep is accepted only for parity. staleArchives
// likewise has no reproduced trigger and stays 0.
func prunePluginRoot(root string, legacy bool, cutoff time.Time, live map[string]bool, res *pruneResult) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if !os.IsNotExist(err) {
			res.Errors = append(res.Errors, root+": "+err.Error())
		}
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !pluginHashRE.MatchString(name) {
			continue // only plugin content-hash directories are swept
		}
		pruneOnePluginDir(root, name, legacy, cutoff, live, res)
	}
}

// pluginRemoveAll is the recursive delete pruneOnePluginDir performs, behind a seam so a
// test can drive the failure arm without arranging a real undeletable directory (a chmod
// fixture is a no-op for a root CI user, and this is a delete path, which the project keeps
// out of a live filesystem in tests). Mirrors hcRemoveAll in hostclean.go.
var pluginRemoveAll = os.RemoveAll

// pruneOnePluginDir classifies one plugin content-hash directory and, when it is
// neither live nor young, removes it. It holds that directory's lock across the
// removal, so two concurrent prunes of the same directory never interleave.
func pruneOnePluginDir(root, name string, legacy bool, cutoff time.Time, live map[string]bool, res *pruneResult) {
	unlock := lockPluginDir(name)
	defer unlock()
	dir := filepath.Join(root, name)
	if live[name] {
		res.Live++
		return
	}
	lastUsed := pluginLastUsed(dir)
	if lastUsed.After(cutoff) {
		res.Young++
		return
	}
	if rerr := pluginRemoveAll(dir); rerr != nil {
		res.Errors = append(res.Errors, dir+": "+rerr.Error())
		return
	}
	if legacy {
		res.PrunedLegacy = append(res.PrunedLegacy, name)
		logInfof("[Plugins] pruned legacy plugin dir %s (last used %s)", dir, lastUsed.Format(time.RFC3339))
	} else {
		res.Pruned = append(res.Pruned, name)
		logInfof("[Plugins] pruned plugin dir %s (last used %s)", dir, lastUsed.Format(time.RFC3339))
	}
}

// pluginDirLocks holds one lock per plugin-directory NAME (not full path).
// Entries are created on first use. claustrum never removes one; the set of
// content-hash names is bounded, so the map stays small.
var (
	pluginDirLocksMu sync.Mutex
	pluginDirLocks   = map[string]*sync.Mutex{}
)

// lockPluginDir acquires the lock for one plugin-directory name and returns its
// release. claustrum exposes only plugins.prune (no plugin-install RPC), so the
// lock serialises concurrent prunes of the same directory, keyed by name so two
// prunes of the same content-hash dir never interleave.
func lockPluginDir(name string) func() {
	pluginDirLocksMu.Lock()
	m := pluginDirLocks[name]
	if m == nil {
		m = &sync.Mutex{}
		pluginDirLocks[name] = m
	}
	pluginDirLocksMu.Unlock()
	m.Lock()
	return m.Unlock
}

// pluginLastUsed is the reference's "last used" time for a plugin dir: the newer of
// the dir's own mtime and its .synced marker's mtime. A missing .synced or a stat
// error contributes nothing, so the dir mtime stands (measured against 19f30c46 —
// the manifest.json mtime does not count, and a fresh .synced keeps an old dir).
func pluginLastUsed(path string) time.Time {
	var t time.Time
	if info, err := os.Stat(path); err == nil {
		t = info.ModTime()
	}
	if info, err := os.Stat(filepath.Join(path, ".synced")); err == nil && info.ModTime().After(t) {
		t = info.ModTime()
	}
	return t
}
