package main

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
)

// newTestGitDeps returns the git state of a server that newTestServer makes:
// no settings control and a count of its own.
func newTestGitDeps() *gitDeps {
	return &gitDeps{OffRequests: new(atomic.Int32)}
}

// gitDispatch feeds a raw line of a git method through the steps that dispatch
// runs for it and returns the marshaled response, as dispatchRaw does. It decodes
// the line, then runs the auth, version and namespace tests of gate and route, then
// handleGit with d. A line that fails one of those tests fails the test, because
// only the server writes those replies.
func gitDispatch(t *testing.T, d *gitDeps, line string) string {
	t.Helper()
	var req request
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		t.Fatalf("gitDispatch: line %s: %v", line, err)
	}
	if req.Auth != testToken {
		t.Fatalf("gitDispatch: line %s has no test token", line)
	}
	if req.JSONRPC != "2.0" {
		t.Fatalf("gitDispatch: line %s is not JSON-RPC 2.0", line)
	}
	if ns, _, ok := strings.Cut(req.Method, "."); !ok || ns != "git" {
		t.Fatalf("gitDispatch: line %s names no git method", line)
	}
	b, err := json.Marshal(handleGit(d, &req))
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	return string(b)
}
