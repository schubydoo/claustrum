package main

import (
	"encoding/json"
	"fmt"
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
	raw, err := gitDispatchErr(d, line)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// gitDispatchErr is gitDispatch for a goroutine that is not the test: it reports
// a failure as an error and calls no method of t.
func gitDispatchErr(d *gitDeps, line string) (string, error) {
	var req request
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		return "", fmt.Errorf("gitDispatch: line %s: %w", line, err)
	}
	if req.Auth != testToken {
		return "", fmt.Errorf("gitDispatch: line %s has no test token", line)
	}
	if req.JSONRPC != "2.0" {
		return "", fmt.Errorf("gitDispatch: line %s is not JSON-RPC 2.0", line)
	}
	if ns, _, ok := strings.Cut(req.Method, "."); !ok || ns != "git" {
		return "", fmt.Errorf("gitDispatch: line %s names no git method", line)
	}
	b, err := json.Marshal(handleGit(d, &req))
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}
	return string(b), nil
}
