package supervisor

import (
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/schubydoo/claustrum/internal/conn"
	"github.com/schubydoo/claustrum/internal/sysproc"
)

// The adopted environment of server.adoptEnv (c9125eb8, measured on Linux, macOS
// and Windows). After a good adopt, a new child starts from the sent entries, not
// from the environment of the daemon. The set is the same for every connection,
// except the session-bound names. A later good adopt replaces the set. A refused one
// keeps it.

// sessionEnvNames and sessionEnvPrefixes are the session-bound names. A sent entry
// of one reaches only the children of the sending connection. A connection with a
// good adopt of its own gets only the entries of its last adopt. A connection with
// none gets the entries of the daemon (c9125eb8, measured on Linux, macOS and
// Windows). A prefix name that no row showed is claustrum's own, not measured.
var (
	sessionEnvNames    = []string{"DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY", "KRB5CCNAME", "XDG_SEAT", "XDG_VTNR", "GPG_TTY", "WSL_INTEROP"}
	sessionEnvPrefixes = []string{"SSH_", "SSH2_", "XDG_SESSION_"}
)

// daemonTempNames are the temp-folder names that a child takes from the daemon,
// never from the sent entries. They count in the set for every connection
// (c9125eb8, measured on Linux, macOS and Windows). The daemon value then takes the
// test of a spawn (DropUnusableTempVars, measured on Linux and macOS). A sent name that the
// daemon lacks reaches no child (claustrum's own, not measured).
var daemonTempNames = []string{"TMPDIR", "TMP", "TEMP", "CLAUDE_CODE_TMPDIR"}

// daemonEnvPrefix starts the names that a child takes from the daemon, never from
// the sent entries. The sent CLAUDE_SSH_X did not reach a child (c9125eb8, measured
// on Linux). The CLAUDE_SSH_DAEMON_CHILD of the daemon did (measured on Linux, macOS
// and Windows). The other names of the prefix follow the same rule (claustrum's own,
// not measured).
const daemonEnvPrefix = "CLAUDE_SSH_"

// adoptDiffNamesMax is the most names that the adopt line lists.
const adoptDiffNamesMax = 40

// adoptedEnv is the stored set of the last good adopt.
type adoptedEnv struct {
	mu  sync.Mutex
	on  bool
	set []string
	// session holds the session-bound entries of the daemon, for a connection
	// with no adopt of its own.
	session []string
}

// TakeAdoptedEnv stores env as the adopted environment and gives the own entries to c.
// It returns the count of the set for every connection, the count of the own
// entries, and the names whose value the adopt added, removed or changed. Before
// the first adopt the old set is the environment of the daemon. The names come in
// name order, at most adoptDiffNamesMax of them. The session-bound names are not
// compared (measured on Linux). Each count counts a name once (measured on Linux,
// macOS and Windows, the second count on Windows). kept is the member of the adopt. noLoginShell is the member of
// the adopt. Without it a spawn of c can start a read again of the login-shell PATH
// (spawnWith).
func (m *Manager) TakeAdoptedEnv(c *conn.Conn, env, kept []string, noLoginShell bool) (every, own int, changed []string) {
	daemon := sysproc.RemoveEnvKey(os.Environ(), "CLAUDE_RPC_TOKEN")
	all, mine := splitAdopted(env, kept, daemon)
	// The own entries of c change under the same lock as the set, and adoptedBase
	// reads both under it. So no spawn of c sees the new set with the old session
	// names of c, or with those of the daemon. The lock order is adopted.mu, then
	// the lock of c.
	m.adopted.mu.Lock()
	old := daemon
	if m.adopted.on {
		old = m.adopted.set
	}
	if c != nil {
		c.SetOwnEnv(mine)
		c.SetReadsLoginPATH(!noLoginShell)
	}
	m.adopted.on, m.adopted.set = true, all
	m.adopted.session = slices.DeleteFunc(slices.Clone(daemon), func(e string) bool { return !isSessionEnvName(envName(e)) })
	m.adopted.mu.Unlock()
	return countEnvNames(all), countEnvNames(mine), adoptDiffNames(old, all)
}

// adoptedBase is the start of the environment of a child of c: the adopted set,
// then the session-bound entries of c, or those of the daemon when c had no good
// adopt of its own. ok is false before the first good adopt.
func (m *Manager) adoptedBase(c *conn.Conn) (base []string, ok bool) {
	m.adopted.mu.Lock()
	on, set, session := m.adopted.on, m.adopted.set, m.adopted.session
	own, adopted := c.OwnEnv()
	m.adopted.mu.Unlock()
	if !on {
		return nil, false
	}
	if adopted {
		session = own
	}
	return osAdoptedBase(append(slices.Clone(set), session...)), true
}

// splitAdopted splits the sent entries into the set for every connection and the
// session-bound entries. A sent name with daemonEnvPrefix or of daemonTempNames is
// dropped. The entries of the daemon with that prefix or of those names go at the
// end of the set. Then comes the entry of the daemon for each name of kept that the
// set lacks (measured on Linux, macOS and Windows: a sent entry wins). A kept
// session-bound name takes no entry (claustrum's own, not measured).
func splitAdopted(env, kept, daemon []string) (all, own []string) {
	for _, e := range env {
		name := envName(e)
		switch {
		case hasEnvPrefix(name, daemonEnvPrefix), isDaemonTempName(name):
		case isSessionEnvName(name):
			own = append(own, e)
		default:
			all = append(all, e)
		}
	}
	for _, e := range daemon {
		if name := envName(e); hasEnvPrefix(name, daemonEnvPrefix) || isDaemonTempName(name) {
			all = append(all, e)
		}
	}
	for _, k := range kept {
		if isSessionEnvName(k) || slices.ContainsFunc(all, func(e string) bool { return sameEnvName(envName(e), k) }) {
			continue
		}
		for _, e := range daemon {
			if sameEnvName(envName(e), k) {
				all = append(all, e)
				break
			}
		}
	}
	return all, own
}

// isSessionEnvName tells if name is a session-bound name on this system.
func isSessionEnvName(name string) bool {
	return slices.ContainsFunc(sessionEnvNames, func(n string) bool { return sameEnvName(n, name) }) ||
		slices.ContainsFunc(sessionEnvPrefixes, func(p string) bool { return hasEnvPrefix(name, p) })
}

// isDaemonTempName tells if name is one of daemonTempNames on this system.
func isDaemonTempName(name string) bool {
	return slices.ContainsFunc(daemonTempNames, func(n string) bool { return sameEnvName(n, name) })
}

// countEnvNames counts the names of env, each name once.
func countEnvNames(env []string) int {
	seen := make(map[string]bool, len(env))
	for _, e := range env {
		seen[envKey(envName(e))] = true
	}
	return len(seen)
}

// adoptDiffNames lists the names whose value differs between old and cur, or that
// only one of them holds. Each name is in the form of envKey.
func adoptDiffNames(old, cur []string) []string {
	o, n := envValues(old), envValues(cur)
	names := []string{}
	for k, v := range o {
		if w, ok := n[k]; !ok || w != v {
			names = append(names, k)
		}
	}
	for k := range n {
		if _, ok := o[k]; !ok {
			names = append(names, k)
		}
	}
	slices.Sort(names)
	if len(names) > adoptDiffNamesMax {
		names = names[:adoptDiffNamesMax]
	}
	return names
}

// envValues maps the key of each name of env, except the session-bound names, to
// its value. A later entry of a name wins.
func envValues(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, e := range env {
		name := envName(e)
		if isSessionEnvName(name) {
			continue
		}
		out[envKey(name)] = e[min(len(name)+1, len(e)):]
	}
	return out
}

// envName is the name of the entry e: the text before its first "=". A Windows
// entry such as "=C:=C:\x" keeps its leading "=" in the name.
func envName(e string) string {
	start := 0
	if strings.HasPrefix(e, "=") {
		start = 1
	}
	if i := strings.IndexByte(e[start:], '='); i >= 0 {
		return e[:start+i]
	}
	return e
}

// sameEnvName tells if a and b name the same variable on this system.
func sameEnvName(a, b string) bool { return envKey(a) == envKey(b) }

// hasEnvPrefix tells if name starts with prefix on this system.
func hasEnvPrefix(name, prefix string) bool {
	return strings.HasPrefix(envKey(name), envKey(prefix))
}
