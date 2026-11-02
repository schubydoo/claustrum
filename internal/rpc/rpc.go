// Package rpc holds the JSON-RPC wire types of the daemon: the reply envelope,
// the error codes, the result structs and the params helpers.
package rpc

import (
	"bytes"
	"encoding/json"
)

// JSON-RPC error codes (probe-verified against the real binary).
const (
	CodeParse        = -32700 // malformed JSON line; response id is null
	CodeInvalidReq   = -32600 // jsonrpc != "2.0" or field absent
	CodeMethod       = -32601 // unknown method / unknown namespace
	CodeInvalidParam = -32602 // bad / missing params
	CodeInternal     = -32603 // internal error
	CodeUnauthorized = -32001 // bad or missing auth token
	// CodeStdinOffsetGap is returned by process.stdin when a caller's offset is
	// ahead of the bytes applied so far — a gap that would drop input. Added by the
	// reference daemon in 7c2f88d with the stdin-offset idempotency contract.
	CodeStdinOffsetGap = -32003 // stdin offset ahead of applied bytes
	// CodeStdinBackpressure is the code the reference emits from process.stdin when
	// the per-process async stdin queue is already full: it rejects the write with
	// this frame rather than blocking the request. Measured against 4534d86 — a
	// producer that outruns a non-reading child gets an error frame once ~16 MiB is
	// queued (measured).
	CodeStdinBackpressure = -32002 // stdin queue full (backpressure)
	// CodeLauncherUnusable is the process.spawn code for a launcher that the
	// checks refuse, and for any launcher on a Windows host. CodeLauncherStart is
	// the code for a launcher that passes the checks but does not start. 89cb6289
	// added both with the managed launcher. -32004 is measured on Linux, macOS
	// and Windows, and -32005 on Linux and macOS.
	CodeLauncherUnusable = -32004
	CodeLauncherStart    = -32005
	// CodePeerRefused is the code of the frame of a caller that the peer check
	// refuses (peercheck.go). 5fd08069 sends it on Linux (measured).
	CodePeerRefused = -32006
)

// Request holds the five wire members of one inbound JSON-RPC line. Package
// main decodes each line into its own request type and builds a Request from
// it. A decode error text names the Go type, and the parse error log line
// holds that text, so the decode stays on the type of package main. Nothing
// decodes or encodes a Request, so its fields have no json tags.
type Request struct {
	JSONRPC string
	ID      interface{}
	Method  string
	Params  json.RawMessage
	Auth    string
}

// Response is one JSON-RPC reply.
type Response struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *Error      `json:"error,omitempty"`
	// NoFrame marks a request that gets no reply. The route of package main then
	// returns nil. Its tag keeps it out of every frame.
	NoFrame bool `json:"-"`
}

// Error is the error member of a reply.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	// Data is absent, not null, when nil, so a frame without it stays as it was.
	Data interface{} `json:"data,omitempty"`
}

// IDForLog renders an id the way the log line always has: as the JSON text that
// will appear on the wire, so `id=1` and `id="abc"` keep their existing shape now
// that ID is a decoded interface{} rather than the raw bytes. An absent id stays
// empty rather than becoming "null", which is what the previous string(req.ID)
// produced for a nil RawMessage.
//
// The marshal error is discarded rather than branched on. id came out of
// json.Unmarshal into an interface{}, so it is a string, float64, bool, nil,
// []interface{} or map[string]interface{} — none of which json.Marshal can fail
// on. Only cycles, channels, funcs and NaN/Inf can, and a decoded JSON document
// produces none of them. On that impossible error b is nil, which renders as the
// same empty string the nil case above returns, so the branch would have been
// unreachable code that says nothing.
func IDForLog(id interface{}) string {
	if id == nil {
		return ""
	}
	b, _ := json.Marshal(id)
	return string(b)
}

// ErrResult builds an error reply. A nil id marshals to null on its own now that
// ID is an interface{}, so the parse-error path needs no special case — but the
// field must stay free of omitempty, or a null id would vanish from the frame.
func ErrResult(id interface{}, code int, msg string) Response {
	return Response{JSONRPC: "2.0", ID: id, Error: &Error{Code: code, Message: msg}}
}

// ErrResultData is ErrResult with an error data member.
func ErrResultData(id interface{}, code int, msg string, data interface{}) Response {
	return Response{JSONRPC: "2.0", ID: id, Error: &Error{Code: code, Message: msg, Data: data}}
}

// OKResult builds a reply with a result.
func OKResult(id interface{}, result interface{}) Response {
	return Response{JSONRPC: "2.0", ID: id, Result: result}
}

// Ptr returns a pointer to a copy of r.
func Ptr(r Response) *Response { return &r }

// NeedParams enforces that a known method was given a params OBJECT. The real
// daemon rejects an ABSENT params field with -32602 "Invalid params" (an empty
// {} is accepted) — and only after confirming the method exists.
func NeedParams(req *Request) *Response {
	if len(req.Params) == 0 {
		return Ptr(ErrResult(req.ID, CodeInvalidParam, "Invalid params"))
	}
	return nil
}

// UnknownMethod is the reply for a method that its namespace does not have.
func UnknownMethod(req *Request) Response {
	return ErrResult(req.ID, CodeMethod, "Unknown method: "+req.Method)
}

// DecodeParams unmarshals req.Params into v. Empty params is treated as {}.
func DecodeParams(req *Request, v interface{}) error {
	if len(req.Params) == 0 {
		return nil
	}
	return json.Unmarshal(req.Params, v)
}

// Bind decodes req.Params into v, returning an Invalid-params response when
// the body is present but mistyped (e.g. a string where a number is expected, or
// a non-object params value). The reference rejects such requests with -32602
// even though encoding/json could partially decode them — matching that, any
// unmarshal error becomes "Invalid params". Absent params is gated earlier by
// NeedParams; unknown fields are ignored by both daemons. Returns nil on success.
// Bind decodes params and expands any leading `~` before the method sees
// them. The parameter type is PathExpander, not `any`, so a params struct that
// has not declared its expansion behavior does not compile. See PathExpander.
func Bind(req *Request, v PathExpander) *Response {
	if err := DecodeParams(req, v); err != nil {
		return Ptr(ErrResult(req.ID, CodeInvalidParam, "Invalid params"))
	}
	v.ExpandPaths()
	return nil
}

// PathExpander is implemented by EVERY params struct, not just those carrying
// paths, and Bind takes it as its parameter type rather than `any`. That
// makes expansion a compile-time obligation: a new params struct does not build
// until its author writes ExpandPaths and, in doing so, decides whether it has
// paths to expand.
//
// A type assertion inside Bind was the obvious design and is the wrong
// one: a new path-bearing struct that forgot the method would fail the assertion
// silently and keep the literal-"~" behavior that expandpath.go exists to remove. The
// failure mode of that bug is not an error — it is a request quietly operating
// on a path named "~", which is how claustrum came to create a literal `~`
// directory inside a user's repo.
//
// Structs with no path fields implement it as a no-op, which doubles as the
// declaration that they were considered.
type PathExpander interface {
	ExpandPaths()
}

// JSONTrueSwitch is a request member that is on for the JSON value true and off for
// every other value, with no error. On 5fd08069 a git.worktree_create with the
// string "true", the number 1, null, false or {} refuses the filter as with no
// member (measured on Linux, and four of them on Windows).
type JSONTrueSwitch bool

// UnmarshalJSON sets b to true for the JSON value true and to false for every
// other value. It never fails.
func (b *JSONTrueSwitch) UnmarshalJSON(data []byte) error {
	*b = string(bytes.TrimSpace(data)) == "true"
	return nil
}
