package supervisor

import (
	"time"

	"github.com/schubydoo/claustrum/internal/rpc"
)

// A given-up id (4c111e86, measured on Linux, macOS and Windows). process.kill,
// process.killAndWait and process.reattach of an id that no process has give that id
// up. A process.spawn of a given-up id is refused, and the command does not run. The
// requests that give an id up answer as before. A given-up id changes no signal.
//
// Not built: an end of a child that started while its id was given up. No row shows
// such an end on any build (Linux 90 pairs, macOS 40, Windows), so claustrum registers
// that child as before.

// givenUpIDCap is the most ids that are given up at once. Once the set is full, a
// new give-up is dropped. After 4100 give-ups one at a time, 4c111e86 refuses ids
// 0 to 4095 and starts the later ones (measured on Linux). The cap counts every
// given-up id: after 20 other give-ups, ids 0 to 4075 are refused. In a burst of
// give-ups the ids near the cap varied from run to run (measured on Linux).
// claustrum's order in a burst is its own.
const givenUpIDCap = 4096

// givenUpIDAge is how long an id stays given up. 4c111e86 refuses the spawn at 840 s
// and starts it at 910 s (measured on Linux and Windows), and on macOS it refuses at
// 14 minutes and starts at 16. 15 minutes is claustrum's own value in that range.
// Copied into the manager at construction. var so tests can shrink it.
var givenUpIDAge = 15 * time.Minute

// givenUpIDMaxBytes is the longest id that is given up. Ids of 1, 127 and 128 bytes
// are given up, and ids of 129 and 200 bytes are not (measured on Linux, and 1, 128
// and 129 on Windows). The count is of bytes (claustrum's own for a non-ASCII id).
const givenUpIDMaxBytes = 128

// errGivenUpID is the refusal of a spawn of a given-up id: -32602 with
// "data":{"reason":"other"} (measured on Linux, macOS and Windows).
var errGivenUpID = &SpawnRefusal{Code: rpc.CodeInvalidParam, Msg: "this id was asked after, or asked to be ended, while no process had it: take a fresh one"}

// GiveUpUnknown gives id up when no process of the table has it and no link-bound
// spawn of id is starting, and the set has room (givenUpIDCap). It signals nothing.
// A full set first drops the ids past their time. If it is still full, a new id is
// dropped, and an id already in the set keeps its old time (4c111e86, measured on
// Linux). In a set that is not full, each call sets the time anew (claustrum's
// own, not measured). The id must match exactly: case and a trailing blank make
// another id (measured on Windows).
func (m *Manager) GiveUpUnknown(id string) {
	if id == "" || len(id) > givenUpIDMaxBytes {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.procs[id] != nil {
		return
	}
	if _, ok := m.linkStarting[id]; ok {
		return
	}
	now := time.Now()
	if len(m.givenUp) >= givenUpIDCap {
		m.pruneGivenUpLocked(now)
		if len(m.givenUp) >= givenUpIDCap {
			return
		}
	}
	m.givenUp[id] = now
}

// givenUpLocked reports whether id is given up. A refused spawn does not renew the
// time (measured on Windows: refused at 840 s, started at 910 s). The caller holds
// m.mu.
func (m *Manager) givenUpLocked(id string) bool {
	_, ok := m.givenUp[id]
	return ok
}

// pruneGivenUpLocked drops the given-up ids older than givenUpAge. The caller holds
// m.mu.
func (m *Manager) pruneGivenUpLocked(now time.Time) {
	for id, at := range m.givenUp {
		if now.Sub(at) >= m.givenUpAge {
			delete(m.givenUp, id)
		}
	}
}
