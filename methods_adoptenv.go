package main

import (
	"strings"

	"github.com/schubydoo/claustrum/internal/conn"
	"github.com/schubydoo/claustrum/internal/logging"
	"github.com/schubydoo/claustrum/internal/rpc"
)

// methodAdoptEnv is server.adoptEnv (c9125eb8, measured on Linux, macOS and
// Windows). A connection sends its environment, and new children start from it.
const methodAdoptEnv = "server.adoptEnv"

// adoptEnvParams are the params of server.adoptEnv. Kept names daemon variables that
// stay in the set when the sent entries lack them, with a limit of its own (measured
// on Linux, macOS and Windows, the limit on Linux and macOS), except a session-bound
// name (claustrum's own, not measured). NoLoginShell is accepted with ok
// (measured on Linux, macOS and Windows). With it true the spawns of the connection
// start no read again of the login-shell PATH (measured on Linux, see spawnWith). Other
// members are ignored (measured on Linux and Windows).
type adoptEnvParams struct {
	Env          []string `json:"env"`
	NoLoginShell bool     `json:"noLoginShell"`
	Kept         []string `json:"kept"`
}

// The limits of an adopt (c9125eb8). Measured on Linux: an entry of 131071 bytes
// passes and one of 131072 fails, 8192 entries pass and 8193 fail, 262132 bytes in
// four entries pass and 300009 fail. The total is the sum of the entry lengths
// (claustrum's own between the two measured totals).
const (
	adoptMaxEntries    = 8192
	adoptMaxEntryBytes = 128 << 10
	adoptMaxTotalBytes = 256 << 10
)

// The log texts of server.adoptEnv (c9125eb8, measured on Linux, macOS and Windows).
const (
	fmtAdoptRefused = "[ServerHandler] server.adoptEnv: refused the environment that a new connection sent (%s). New processes keep starting from the previous environment."
	fmtAdopted      = "[Server] New processes now start from the environment sent on connection %s: %d variables for every connection, %d for this connection only. Names added, removed or changed (at most 40): %q"
)

// adoptEnvProblem is the reason text of a refused adopt, or "" for a good one. The
// texts are measured. The tests of each entry come before the count, and in one
// entry a NUL comes before a missing "=" (measured on Linux, macOS and Windows).
// The other orders are claustrum's own, not measured.
func adoptEnvProblem(env, kept []string) string {
	if len(env) == 0 {
		return "it is empty"
	}
	total := 0
	for _, e := range env {
		switch {
		case strings.Contains(e, "\x00"):
			return "an entry contains a NUL byte"
		case !strings.Contains(e, "="):
			return "an entry has no equals sign"
		case len(e) >= adoptMaxEntryBytes:
			return "one variable is 128 KiB or longer, more than Linux allows when it starts a program"
		}
		total += len(e)
	}
	if len(env) > adoptMaxEntries {
		return "it has more than 8192 entries"
	}
	if total > adoptMaxTotalBytes {
		return "it is larger than 256 KiB in total"
	}
	if len(kept) > adoptMaxEntries {
		return "it names more than 8192 variables to keep"
	}
	return ""
}

// handleAdoptEnv answers server.adoptEnv. These shapes give -32602 with no line:
// absent params (measured on Linux, macOS and Windows), an env that is a string (Linux,
// macOS and Windows) and entries that are not strings (Linux). Other params that do
// not decode give the same (claustrum's own, not measured). A literal null gives the
// line of an empty list (measured on Linux, macOS and Windows). A refused list gives
// -32602 and the refusal line, and the adopted set stays. A good list replaces the
// set and answers ok.
func (s *server) handleAdoptEnv(c *conn.Conn, req *request) *rpc.Response {
	if len(req.Params) == 0 {
		return rpc.Ptr(rpc.ErrResult(req.ID, rpc.CodeInvalidParam, "Invalid params"))
	}
	// A literal null decodes to no member, so its list is empty.
	var p adoptEnvParams
	if err := decodeParams(req, &p); err != nil {
		return rpc.Ptr(rpc.ErrResult(req.ID, rpc.CodeInvalidParam, "Invalid params"))
	}
	if why := adoptEnvProblem(p.Env, p.Kept); why != "" {
		logging.Warnf(fmtAdoptRefused, why)
		return rpc.Ptr(rpc.ErrResult(req.ID, rpc.CodeInvalidParam, "Invalid params"))
	}
	every, own, changed := s.procs.TakeAdoptedEnv(c, p.Env, p.Kept, p.NoLoginShell)
	label := `""`
	if c != nil {
		label = c.RemoteLabel()
	}
	logging.Infof(fmtAdopted, label, every, own, changed)
	return rpc.Ptr(rpc.OKResult(req.ID, rpc.AdoptEnvResult{OK: true}))
}
