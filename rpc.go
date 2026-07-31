package main

import (
	"crypto/subtle"
	"encoding/json"
	"strings"
)

// JSON-RPC error codes (probe-verified against the real binary).
const (
	codeParse        = -32700 // malformed JSON line; response id is null
	codeInvalidReq   = -32600 // jsonrpc != "2.0" or field absent
	codeMethod       = -32601 // unknown method / unknown namespace
	codeInvalidParam = -32602 // bad / missing params
	codeInternal     = -32603 // internal error
	codeUnauthorized = -32001 // bad or missing auth token
	// codeStdinOffsetGap is returned by process.stdin when a caller's offset is
	// ahead of the bytes applied so far — a gap that would drop input. Added by the
	// reference daemon in 7c2f88d with the stdin-offset idempotency contract.
	codeStdinOffsetGap = -32003 // stdin offset ahead of applied bytes
)

// request is one inbound JSON-RPC line.
//
// id is decoded into an interface{}, NOT a json.RawMessage, so the reply carries
// the id Go re-encodes rather than the bytes the client sent. That is what the
// reference does — its rpc.Request.ID is an interface{} — and the difference is
// observable. Probe-measured against 5db5e4a:
//
//	sent 1.0                   -> reference replies 1
//	sent 1e2                   -> 100
//	sent 12345678901234567890  -> 12345678901234567000   (float64, so precision is lost)
//	sent {"b":1,"a":2}         -> {"a":2,"b":1}          (map, so keys sort)
//
// Echoing the raw bytes reproduced none of those. Plain interface{} reproduces
// all four exactly, including the precision loss — which is itself the evidence
// the reference does not use a json.Decoder with UseNumber.
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

type response struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// idForLog renders an id the way the log line always has: as the JSON text that
// will appear on the wire, so `id=1` and `id="abc"` keep their existing shape now
// that ID is a decoded interface{} rather than the raw bytes. An absent id stays
// empty rather than becoming "null", which is what the previous string(req.ID)
// produced for a nil RawMessage.
func idForLog(id interface{}) string {
	if id == nil {
		return ""
	}
	b, err := json.Marshal(id)
	if err != nil {
		return ""
	}
	return string(b)
}

// errResult builds an error reply. A nil id marshals to null on its own now that
// ID is an interface{}, so the parse-error path needs no special case — but the
// field must stay free of omitempty, or a null id would vanish from the frame.
func errResult(id interface{}, code int, msg string) response {
	return response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

func okResult(id interface{}, result interface{}) response {
	return response{JSONRPC: "2.0", ID: id, Result: result}
}

// dispatch validates and routes one request. It returns the response to send, or
// nil when the method must produce no reply (server.shutdown closes silently).
// Stream-producing methods (process.*) use the conn to attach the client.
func (s *server) dispatch(c *conn, raw []byte) *response {
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		return ptr(errResult(nil, codeParse, "Parse error"))
	}
	// Precedence is auth → version (probe-verified): a request that fails BOTH
	// (e.g. no auth and no/!="2.0" jsonrpc) reports Unauthorized, not the version
	// error. Only once auth passes is the version validated.
	//
	// The token compare is constant-time (crypto/subtle) so the auth path can't
	// leak the count of matching leading bytes through response latency — defense-
	// in-depth (HackerOne #3793038); the local 0600 socket already gates any such
	// oracle to the token's own user. The empty-auth short-circuit only fast-rejects
	// an obvious miss and reveals nothing about the token; ConstantTimeCompare
	// returns 0 on a length mismatch, so a wrong-length token is still rejected.
	if req.Auth == "" || subtle.ConstantTimeCompare([]byte(req.Auth), []byte(s.token)) != 1 {
		logWarnf("[Server] Unauthorized request: method=%s, id=%v", req.Method, idForLog(req.ID))
		return ptr(errResult(req.ID, codeUnauthorized, "Unauthorized: invalid or missing auth token"))
	}
	if req.JSONRPC != "2.0" {
		return ptr(errResult(req.ID, codeInvalidReq, "Invalid JSON-RPC version"))
	}

	ns, _, ok := strings.Cut(req.Method, ".")
	if !ok {
		// A method without a "namespace.method" shape is a format error, distinct
		// from a well-formed method naming an unknown namespace (below).
		return ptr(errResult(req.ID, codeMethod, "Invalid method format: "+req.Method))
	}

	switch ns {
	case "server":
		return s.handleServer(c, &req) // may return nil (shutdown)
	case "files":
		return ptr(s.handleFiles(&req))
	case "git":
		return ptr(s.handleGit(&req))
	case "process":
		return ptr(s.handleProcess(c, &req))
	default:
		return ptr(errResult(req.ID, codeMethod, "Unknown namespace: "+ns))
	}
}

func ptr(r response) *response { return &r }

// needParams enforces that a known method was given a params OBJECT. The real
// daemon rejects an ABSENT params field with -32602 "Invalid params" (an empty
// {} is accepted) — and only after confirming the method exists.
func needParams(req *request) *response {
	if len(req.Params) == 0 {
		return ptr(errResult(req.ID, codeInvalidParam, "Invalid params"))
	}
	return nil
}

func unknownMethod(req *request) response {
	return errResult(req.ID, codeMethod, "Unknown method: "+req.Method)
}

// decodeParams unmarshals req.Params into v. Empty params is treated as {}.
func decodeParams(req *request, v interface{}) error {
	if len(req.Params) == 0 {
		return nil
	}
	return json.Unmarshal(req.Params, v)
}

// bindParams decodes req.Params into v, returning an Invalid-params response when
// the body is present but mistyped (e.g. a string where a number is expected, or
// a non-object params value). The reference rejects such requests with -32602
// even though encoding/json could partially decode them — matching that, any
// unmarshal error becomes "Invalid params". Absent params is gated earlier by
// needParams; unknown fields are ignored by both daemons. Returns nil on success.
// bindParams decodes params and expands any leading `~` before the method sees
// them. The parameter type is pathExpander, not `any`, so a params struct that
// has not declared its expansion behavior does not compile — see expandpath.go
// for why a type assertion here would have been the wrong design.
func bindParams(req *request, v pathExpander) *response {
	if err := decodeParams(req, v); err != nil {
		return ptr(errResult(req.ID, codeInvalidParam, "Invalid params"))
	}
	v.expandPaths()
	return nil
}
