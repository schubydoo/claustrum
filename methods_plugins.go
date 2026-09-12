package main

import (
	"encoding/json"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/schubydoo/claustrum/handlers"
)

// plugins.prune (reference build 19f30c46) prunes cached CLI plugin directories
// that no active session references and that have not been used within minAgeDays.
// A plugin dir is named by its content hash (16 lowercase hex). Two roots are
// swept: the per-install root plugins/<clientId>/ (results in `pruned`) and the
// shared legacy root plugins/ (results in `prunedLegacy`), both relative to the
// daemon socket's run/<clientId>/rpc.sock layout. The per-install sweep always
// runs; the legacy sweep is skipped while any sibling daemon on the host answers a
// dial, since its sessions may reference the shared legacy plugins.
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
	// Shared legacy root -> prunedLegacy[]. "absent" when the shared dir does not
	// exist. When a SIBLING daemon on this host answers a dial, its sessions may
	// reference legacy plugins, so the legacy sweep is skipped and the reason names
	// the first sibling that answered. Otherwise "swept" and the legacy dirs are
	// classified like the per-install root.
	base := filepath.Dir(legacyRoot) // <X>: the parent of run/ and plugins/
	if _, err := os.Stat(legacyRoot); err != nil {
		res.Legacy = "absent"
	} else if sib := siblingDaemonAlive(base, filepath.Base(root)); sib != "" {
		res.Legacy = "skipped: another install's daemon is running (" + sib + " answers); its sessions may use the shared legacy dirs"
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

// siblingDaemonAlive reports the first sibling daemon — a run/<clientId>/rpc.sock
// under base other than own — that answers a 300ms unix dial, returned as the
// socket path relative to base ("run/<clientId>/rpc.sock"), or "" when none
// answers. The legacy sweep is skipped while a sibling is alive, because its
// sessions may reference the shared legacy plugins. os.ReadDir is sorted, so the
// first sibling in clientId order that answers is the one named.
func siblingDaemonAlive(base, own string) string {
	entries, err := os.ReadDir(filepath.Join(base, "run"))
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == own {
			continue
		}
		sock := filepath.Join(base, "run", e.Name(), "rpc.sock")
		if c, derr := net.DialTimeout("unix", sock, 300*time.Millisecond); derr == nil {
			_ = c.Close()
			// The return value is a wire string embedded in the skip message, not a
			// filesystem path, so it uses forward slashes on every OS (path.Join, not
			// filepath.Join) — matching the reference's "run/<clientId>/rpc.sock".
			return path.Join("run", e.Name(), "rpc.sock")
		}
	}
	return ""
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
// any sibling answers a dial, matching the reference.
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
		path := filepath.Join(root, name)
		if live[name] {
			res.Live++
			continue
		}
		lastUsed := pluginLastUsed(path)
		if lastUsed.After(cutoff) {
			res.Young++
			continue
		}
		if rerr := os.RemoveAll(path); rerr != nil {
			res.Errors = append(res.Errors, path+": "+rerr.Error())
			continue
		}
		if legacy {
			res.PrunedLegacy = append(res.PrunedLegacy, name)
			logInfof("[Plugins] pruned legacy plugin dir %s (last used %s)", path, lastUsed.Format(time.RFC3339))
		} else {
			res.Pruned = append(res.Pruned, name)
			logInfof("[Plugins] pruned plugin dir %s (last used %s)", path, lastUsed.Format(time.RFC3339))
		}
	}
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
