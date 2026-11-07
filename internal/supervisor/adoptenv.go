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
// except the own names, which reach only the children of the connection that sent
// them. A later good adopt replaces the set. A refused one keeps it.

// ownEnvNames are the names that reach only the children of the sending connection.
// Linux showed SSH_AUTH_SOCK so (c9125eb8). No other own name was found.
var ownEnvNames = []string{"SSH_AUTH_SOCK"}

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
}

// TakeAdoptedEnv stores env as the adopted environment and gives the own entries to c.
// It returns the count of the set for every connection, the count of the own
// entries, and the names whose value the adopt added, removed or changed. Before
// the first adopt the old set is the environment of the daemon. The names come in
// name order, at most adoptDiffNamesMax of them. The own names are not compared
// (measured on Linux: the line did not name a sent SSH_AUTH_SOCK).
func (m *Manager) TakeAdoptedEnv(c *conn.Conn, env []string) (every, own int, changed []string) {
	daemon := sysproc.RemoveEnvKey(os.Environ(), "CLAUDE_RPC_TOKEN")
	all, mine := splitAdopted(env, daemon)
	m.adopted.mu.Lock()
	old := daemon
	if m.adopted.on {
		old = m.adopted.set
	}
	m.adopted.on, m.adopted.set = true, all
	m.adopted.mu.Unlock()
	if c != nil {
		c.SetOwnEnv(mine)
	}
	return len(all), len(mine), adoptDiffNames(old, all)
}

// adoptedBase is the start of the environment of a child of c: the adopted set,
// then the own entries of c. ok is false before the first good adopt.
func (m *Manager) adoptedBase(c *conn.Conn) (base []string, ok bool) {
	m.adopted.mu.Lock()
	on, set := m.adopted.on, m.adopted.set
	m.adopted.mu.Unlock()
	if !on {
		return nil, false
	}
	return osAdoptedBase(append(slices.Clone(set), c.OwnEnv()...)), true
}

// splitAdopted splits the sent entries into the set for every connection and the
// own entries. A sent name with daemonEnvPrefix is dropped. The entries of the
// daemon with that prefix go at the end of the set, then the names that
// osKeptDaemonNames keeps.
func splitAdopted(env, daemon []string) (all, own []string) {
	for _, e := range env {
		name := envName(e)
		switch {
		case hasEnvPrefix(name, daemonEnvPrefix):
		case slices.ContainsFunc(ownEnvNames, func(n string) bool { return sameEnvName(n, name) }):
			own = append(own, e)
		default:
			all = append(all, e)
		}
	}
	for _, e := range daemon {
		if hasEnvPrefix(envName(e), daemonEnvPrefix) {
			all = append(all, e)
		}
	}
	for _, kept := range osKeptDaemonNames {
		if slices.ContainsFunc(all, func(e string) bool { return sameEnvName(envName(e), kept) }) {
			continue
		}
		for _, e := range daemon {
			if sameEnvName(envName(e), kept) {
				all = append(all, e)
				break
			}
		}
	}
	return all, own
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

// envValues maps the key of each name of env, except the own names, to its value.
// A later entry of a name wins.
func envValues(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, e := range env {
		name := envName(e)
		if slices.ContainsFunc(ownEnvNames, func(n string) bool { return sameEnvName(n, name) }) {
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
