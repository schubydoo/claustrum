package main

import (
	"crypto/subtle"
	"encoding/json"
	"strings"

	"github.com/schubydoo/claustrum/internal/files"
	"github.com/schubydoo/claustrum/internal/git"
	"github.com/schubydoo/claustrum/internal/logging"
	"github.com/schubydoo/claustrum/internal/rpc"
	"github.com/schubydoo/claustrum/internal/sessiondirs"
)

// request is one inbound JSON-RPC line.
//
// id is decoded into an interface{}, NOT a json.RawMessage, so the reply carries
// the id Go re-encodes rather than the bytes the client sent. The reference
// replies the same way, and the difference is observable. Probe-measured
// against 5db5e4a:
//
//	sent 1.0                   -> reference replies 1
//	sent 1e2                   -> 100
//	sent 12345678901234567890  -> 12345678901234567000   (float64, so precision is lost)
//	sent {"b":1,"a":2}         -> {"a":2,"b":1}          (map, so keys sort)
//
// Echoing the raw bytes reproduced none of those. Plain interface{} reproduces
// all four exactly, including the precision loss.
//
// Integers, strings, arrays and null were already identical: encoding/json
// compacts and HTML-escapes a RawMessage too, so "a<b" came back "a\u003cb"
// either way.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Auth    string          `json:"auth"`
}

// wire returns the five wire members of req as an rpc.Request.
func (req *request) wire() *rpc.Request {
	return &rpc.Request{JSONRPC: req.JSONRPC, ID: req.ID, Method: req.Method, Params: req.Params, Auth: req.Auth}
}

func needParams(req *request) *rpc.Response { return rpc.NeedParams(req.wire()) }

func unknownMethod(req *request) rpc.Response { return rpc.UnknownMethod(req.wire()) }

func decodeParams(req *request, v interface{}) error { return rpc.DecodeParams(req.wire(), v) }

func bindParams(req *request, v rpc.PathExpander) *rpc.Response { return rpc.Bind(req.wire(), v) }

// methodShutdown is the one method exempt from the auth gate — see gate.
//
// The value is also ON THE WIRE: capabilityMethods embeds it, so it is emitted
// as server.capabilities → result.methods[3]. Changing this string for an auth
// reason would silently move that frame; it is not an internal identifier.
const methodShutdown = "server.shutdown"

// dispatch validates and routes one request and returns the response to send.
// Only tests call it. In production serveConn runs gate on the read loop and
// handleRequest runs route through dispatchRequest, so dispatch is a test seam
// for the same two steps.
// It returns nil for a process.spawn that reached its start after the shutdown
// began. Every other path that returns gives a frame, server.shutdown included:
// {"ok":true}. That frame races the teardown (see handleServer). A handler panic
// does not return here. The recover in handleRequest then leaves resp nil for
// server.shutdown, so that request gets no frame.
// Stream-producing methods (process.*) use the conn to attach the client.
func (s *server) dispatch(c *conn, raw []byte) *rpc.Response {
	req, resp := s.gate(c, raw)
	if resp != nil {
		return resp
	}
	return s.route(c, req)
}

// route handles a request that already passed gate. serveConn calls it with the
// request its read loop decoded, so an accepted line is parsed only once.
func (s *server) route(c *conn, req request) *rpc.Response {
	if req.JSONRPC != "2.0" {
		return rpc.Ptr(rpc.ErrResult(req.ID, rpc.CodeInvalidReq, "Invalid JSON-RPC version"))
	}

	ns, _, ok := strings.Cut(req.Method, ".")
	if !ok {
		// A method without a "namespace.method" shape is a format error, distinct
		// from a well-formed method naming an unknown namespace (below).
		return rpc.Ptr(rpc.ErrResult(req.ID, rpc.CodeMethod, "Invalid method format: "+req.Method))
	}

	// "sessionFoldersOff":true turns the check of session folders off while the
	// request runs (b3081b51, measured on Linux for each git method and
	// process.spawn, on macOS and Windows for git.status and process.spawn).
	// The hold is global: a request without the switch that runs during it gets no
	// check either (b3081b51, measured on Linux). The on-again line comes at the
	// first check after it (sessiondirs.on).
	if (ns == "git" || req.Method == "process.spawn") && sessiondirs.OffSwitchSet(req.Params) {
		defer sessiondirs.PauseCheck()()
	}

	switch ns {
	case "server":
		return s.handleServer(c, &req)
	case "files":
		return rpc.Ptr(files.Handle(req.wire()))
	case "git":
		return rpc.Ptr(git.Handle(s.gitDeps(), req.wire()))
	case "process":
		r := s.handleProcess(c, &req)
		if r.NoFrame {
			return nil
		}
		return &r
	case "plugins":
		return rpc.Ptr(s.handlePlugins(&req))
	case "launcher":
		return rpc.Ptr(s.handleLauncher(&req))
	default:
		return rpc.Ptr(rpc.ErrResult(req.ID, rpc.CodeMethod, "Unknown namespace: "+ns))
	}
}

// gitDeps returns the git state of the server: its settings control and a
// pointer to its own count, never a copy.
func (s *server) gitDeps() *git.Deps {
	return &git.Deps{Shadow: s.gitShadow, OffRequests: &s.offGitRequests}
}

// gate runs the first two steps of dispatch: the parse check, then the auth
// check. It returns the decoded request. It also returns the -32700 or -32001
// reply when raw fails one of the two checks, and nil when raw passes both.
//
// dispatch and serveConn both call gate, so the two cannot disagree about
// these frames. serveConn writes a gate reply on the read path, before it
// reads the next line. A line that passes gate goes to the async dispatch.
//
// c is the connection of the line. A request that passes the token check sets the
// token mark of c (conn.showToken), and a nil c sets none.
func (s *server) gate(c *conn, raw []byte) (request, *rpc.Response) {
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		// Logged here, before serveConn writes the reply. The reference logs
		// "Parse error: <json error>" before its reply write (measured). Claustrum
		// counts the lines and logs the sample (logSampled, claustrum's own, not measured).
		if n := s.parseLines.Add(1); logSampled(n) {
			logging.Warnf(fmtParseError, n, err)
		}
		return req, rpc.Ptr(rpc.ErrResult(nil, rpc.CodeParse, "Parse error"))
	}
	// Precedence is auth → version (probe-verified) for every method that IS
	// authenticated: a request that fails BOTH (e.g. no auth and no/!="2.0"
	// jsonrpc) reports Unauthorized, not the version error. Only once auth passes
	// is the version validated.
	//
	// Stated without that qualifier this was wrong: server.shutdown skips the auth
	// gate entirely. The exemption's boundary — auth only, version still applies —
	// is recorded with its measurement at the gate itself, below.
	//
	// The token compare is constant-time (crypto/subtle) so the auth path can't
	// leak the count of matching leading bytes through response latency — defense-
	// in-depth. On Linux and macOS the local 0600 socket already gates any such
	// oracle to the token's own user. The empty-auth short-circuit only fast-rejects
	// an obvious miss and reveals nothing about the token; ConstantTimeCompare
	// returns 0 on a length mismatch, so a wrong-length token is still rejected.
	//
	// server.shutdown is the ONE exemption: the reference does not authenticate
	// it. Measured at 5db5e4a — a shutdown frame whose auth member is absent,
	// wrong, or arbitrary all stop the daemon, while every OTHER method rejects an
	// unauthenticated request with -32001 on both daemons (all 19 swept, only this
	// one differed).
	//
	// This is deliberately NOT a hardening claustrum keeps. The Desktop client
	// tears the daemon down by invoking `server --stop --socket <sock>` with no
	// CLAUDE_RPC_TOKEN in its environment, so a claustrum that demands auth
	// here cannot be stopped by it at all — and being swappable for the
	// reference under Desktop is the whole point of the version-spoofing conf.
	// clauster is unaffected in either direction: it shuts down over its own
	// authenticated RPC connection, and an auth member that IS present is simply
	// ignored here rather than rejected.
	//
	// The exemption covers auth ONLY. The version check in route still applies:
	// measured, a shutdown frame with jsonrpc "1.0" or absent yields -32600 and
	// the reference stays up.
	if req.Method != methodShutdown &&
		(req.Auth == "" || subtle.ConstantTimeCompare([]byte(req.Auth), []byte(s.token)) != 1) {
		// Claustrum's own, not measured: the id is not logged, the method is cut to
		// 64 characters and quoted, and only the sampled counts log.
		if n := s.unauthLines.Add(1); logSampled(n) {
			logging.Warnf(fmtUnauth, n, req.Method)
		}
		return req, rpc.Ptr(rpc.ErrResult(req.ID, rpc.CodeUnauthorized, "Unauthorized: invalid or missing auth token"))
	}
	c.showToken()
	// A request past the auth step restarts the idle time of the idle exit, except
	// server.capabilities (see noteCountedRequest).
	if req.Method != "server.capabilities" {
		s.noteCountedRequest()
	}
	return req, nil
}
