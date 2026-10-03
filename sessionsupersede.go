package main

import (
	"errors"
	"io/fs"
	"os/exec"
	"strings"
)

// missingCommandError is the pre-check of a session spawn. It returns the error of a
// command that does not exist, or nil. spawn runs it after the environment is built
// and before the supersede, so such a spawn sends the prior process of its session no
// signal.
//
// Measured on Linux with strace against 89cb6289. claustrum build ac5cadb gave the
// same stat calls and the same replies in each of these rows:
//   - A session spawn whose command path does not exist answers "fork/exec <path>: no
//     such file or directory". The prior process of the session gets no signal, lives
//     on and keeps its record (rows P3 and H1, and macOS row P3).
//   - The test is one stat of the path as the request gives it. It is not joined with
//     the cwd of the request: "./tool" that exists only under that cwd answers
//     "fork/exec ./tool: no such file or directory" (row H3).
//   - A spawn with no session key runs no stat. The same "./tool" request starts
//     there (rows H2 and H3).
//   - The stat tests existence only. A file of mode 0644, a directory and a script
//     with a missing interpreter pass it: the prior process gets its SIGTERM, and the
//     error of the start comes after (rows H4a, H4b and H4c).
//   - A bare command name that the lookup did not find answers the lookup error, and
//     the prior process gets no signal (row H9).
//   - The cwd test comes first, then the login-shell PATH read of the first spawn,
//     then this stat (rows H3, H6 and H1).
//
// Windows is in statSpawnCommand of that system (rows SSm-path, SSm-bare, SSm-noext).
// Not measured: a stat error other than a missing file. Its text answers.
func missingCommandError(cmd *exec.Cmd) error {
	if cmd.Err != nil {
		return cmd.Err
	}
	if err := statSpawnCommand(cmd.Path); err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			return &fs.PathError{Op: "fork/exec", Path: cmd.Path, Err: pe.Err}
		}
		return err
	}
	return nil
}

// cliSessionKey returns the CLI session id a spawn's argv belongs to, or "" when
// the spawn is not a supersedable session. A non-empty key requires ALL of —
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
// "-" and uses only [A-Za-z0-9-_.:] — the session-id shape claustrum accepts. It
// keeps a stray flag (e.g. "--session-id" followed by another flag) from being read
// as an id.
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

// supersedeSession ends every OTHER running managed process that shares the session
// key of the spawn newID, and waits for each one: a new stream-json session process
// takes the place of the prior one (4534d86). spawn calls it before it starts the new
// process, so the reply of the spawn comes after the end of the victim.
//
// Each victim gets killAndWait with the default grace: SIGTERM, then after 3 s a
// SIGKILL to its group. Measured against 89cb6289 on Linux and macOS (rows B1 to B4):
//
//   - A victim that exits on SIGTERM: the reply follows its exit frame at once (B1).
//     Its group gets no SIGKILL (Linux row X2a).
//   - A victim that ignores SIGTERM: SIGKILL to the group after 3 s, then the reply (B2).
//   - A victim that exits while a process in another group holds its pipes: the
//     reply follows its exit frame, 5 s after the request (B3).
//   - A spawn with the id and the session of a process that still runs supersedes
//     nothing and sends no signal (B4). The id test below gives that.
//
// The log order of the rows is: the exit line of the victim, the supersedes line,
// then the started line of the new process. The exit frame of the victim reaches its
// client with killedBy "client".
//
// On Linux and macOS the escalation ends the whole tree of the victim. On Windows the
// kill ends the direct child only, and its descendants live on. A Windows VM measured
// the same order against 89cb6289 (rows SS and SSInh, 3 runs each): the reply of the
// new spawn comes after the exit frame of the victim.
//
// Not measured: two or more victims of one spawn. claustrum ends them one after the
// other.
func (m *procManager) supersedeSession(key, newID string) {
	if key == "" {
		return
	}
	// Capture the victim PROCESSES, not their client-visible ids. A concurrent
	// spawn that reuses a victim's id replaces m.procs[id]. A kill that resolves the id
	// again then ends the innocent replacement instead. killAndWaitProc
	// signals the captured identity directly.
	m.mu.Lock()
	var victims []*managedProc
	for id, p := range m.procs {
		if id != newID && p.sessionKey == key && p.isRunning() {
			victims = append(victims, p)
		}
	}
	m.mu.Unlock()

	for _, vp := range victims {
		_, died, alreadyExited, escalated := m.killAndWaitProc(vp, "SIGTERM", m.supersedeGrace, true)
		logInfof("[process.Manager] process %s supersedes %s for session %s: died=%v escalated=%v alreadyExited=%v",
			newID, vp.id, key, died, escalated, alreadyExited)
	}
}
