package main

import (
	"encoding/json"
	"fmt"
	"os"
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
)

// request is one inbound JSON-RPC line. id is kept raw so we can echo it back
// verbatim (numbers, strings) and emit null on parse failure.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Auth    string          `json:"auth"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func errResult(id json.RawMessage, code int, msg string) response {
	if id == nil {
		id = json.RawMessage("null")
	}
	return response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

func okResult(id json.RawMessage, result interface{}) response {
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
	if req.JSONRPC != "2.0" {
		return ptr(errResult(req.ID, codeInvalidReq, "Invalid JSON-RPC version"))
	}
	if req.Auth == "" || req.Auth != s.token {
		fmt.Fprintf(os.Stderr, "[Server] Unauthorized request: method=%s, id=%v\n", req.Method, string(req.ID))
		return ptr(errResult(req.ID, codeUnauthorized, "Unauthorized: invalid or missing auth token"))
	}

	ns, _, ok := strings.Cut(req.Method, ".")
	if !ok {
		return ptr(errResult(req.ID, codeMethod, "Unknown namespace: "+req.Method))
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
