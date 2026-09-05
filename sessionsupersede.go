package main

import (
	"strings"
	"time"
)

// cliSessionKey returns the CLI session id a spawn's argv belongs to, or "" when
// the spawn is not a supersedable session. It reproduces the reference's rule
// (4534d86): a non-empty key requires ALL of —
//   - stream-json mode: an arg exactly "--input-format=stream-json",
//     "--output-format=stream-json", or bare "stream-json";
//   - a session id: "--session-id"/"--session-id=<v>" wins; otherwise
//     "--resume"/"--resume=<v>", but the resume fallback is suppressed when
//     "--fork-session" is present (a fork is a NEW session, not a supersede);
//   - a valid token (see validSessionToken).
//
// The key IS the session-id / resume value. A non-stream-json spawn, or an
// invalid/absent token, yields "" and never supersedes.
func cliSessionKey(args []string) string {
	streamJSON := false
	forkSession := false
	sessionID := ""
	resume := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--input-format=stream-json" || a == "--output-format=stream-json" || a == "stream-json":
			streamJSON = true
		case a == "--fork-session":
			forkSession = true
		case a == "--session-id":
			if i+1 < len(args) {
				sessionID = args[i+1]
			}
		case strings.HasPrefix(a, "--session-id="):
			sessionID = strings.TrimPrefix(a, "--session-id=")
		case a == "--resume":
			if i+1 < len(args) {
				resume = args[i+1]
			}
		case strings.HasPrefix(a, "--resume="):
			resume = strings.TrimPrefix(a, "--resume=")
		}
	}
	if !streamJSON {
		return ""
	}
	token := sessionID
	if token == "" && !forkSession {
		token = resume
	}
	if !validSessionToken(token) {
		return ""
	}
	return token
}

// validSessionToken accepts a session id of length 1..128 that does not start with
// "-" and uses only [A-Za-z0-9-_.:] — the session-id shape claustrum accepts, matching
// the reference, which keeps a stray flag (e.g. "--session-id" followed by another
// flag) from being read as an id.
func validSessionToken(t string) bool {
	if len(t) < 1 || len(t) > 128 || strings.HasPrefix(t, "-") {
		return false
	}
	for _, r := range t {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == ':':
		default:
			return false
		}
	}
	return true
}

// lockSessionSpawn serializes concurrent spawns of the same session key and returns
// the unlock func. A per-key mutex is ref-counted so its map entry is dropped once
// no spawn holds or awaits it. key must be non-empty.
func (m *procManager) lockSessionSpawn(key string) func() {
	m.spawnMu.Lock()
	sl := m.spawnLocks[key]
	if sl == nil {
		sl = &sessionSpawnLock{}
		m.spawnLocks[key] = sl
	}
	sl.ref++
	m.spawnMu.Unlock()

	sl.mu.Lock()
	return func() {
		sl.mu.Unlock()
		m.spawnMu.Lock()
		sl.ref--
		if sl.ref == 0 {
			delete(m.spawnLocks, key)
		}
		m.spawnMu.Unlock()
	}
}

// supersedeSession terminates every OTHER running managed process that shares the
// session key of the just-spawned newID, matching the reference (4534d86): a new
// stream-json session process evicts the prior one. Each victim is killed in its
// own goroutine via killAndWait (SIGTERM, grace, then SIGKILL) so a slow teardown
// does not block the spawn. The victim's exit frame reaches its client, carrying
// killedBy once that field ships.
func (m *procManager) supersedeSession(key, newID string) {
	if key == "" {
		return
	}
	m.mu.Lock()
	var victims []string
	for id, p := range m.procs {
		if id != newID && p.sessionKey == key && p.isRunning() {
			victims = append(victims, id)
		}
	}
	m.mu.Unlock()

	grace := time.Duration(defaultKillWaitMs) * time.Millisecond
	for _, vid := range victims {
		go func(vid string) {
			_, died, alreadyExited, escalated := m.killAndWait(vid, "SIGTERM", grace, true)
			logInfof("[process.Manager] process %s supersedes %s for session %s: died=%v escalated=%v alreadyExited=%v",
				newID, vid, key, died, escalated, alreadyExited)
		}(vid)
	}
}
