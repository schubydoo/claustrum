package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/rpc"
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
	var req rpc.Request
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

// gitRequests runs git requests on one *gitDeps, each in a goroutine of its
// own, as the read loop of a connection dispatches them. It keeps each reply
// under the id that the test gave the request.
type gitRequests struct {
	d       *gitDeps
	wg      sync.WaitGroup
	mu      sync.Mutex
	replies map[int]gitReply
}

// gitReply is the reply of one request, or the error of its dispatch.
type gitReply struct {
	raw string
	err error
}

func newGitRequests(d *gitDeps) *gitRequests {
	return &gitRequests{d: d, replies: map[int]gitReply{}}
}

// start dispatches line in a new goroutine under the id n.
func (g *gitRequests) start(n int, line string) {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		raw, err := gitDispatchErr(g.d, line)
		g.mu.Lock()
		g.replies[n] = gitReply{raw, err}
		g.mu.Unlock()
	}()
}

// raw is the reply with the id n, or "" before it came.
func (g *gitRequests) raw(t *testing.T, n int) string {
	t.Helper()
	g.mu.Lock()
	r, ok := g.replies[n]
	g.mu.Unlock()
	if ok && r.err != nil {
		t.Fatal(r.err)
	}
	return r.raw
}

// count is the count of replies so far.
func (g *gitRequests) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.replies)
}

// reply waits up to 60 s for the reply with the id n.
func (g *gitRequests) reply(t *testing.T, n int) string {
	t.Helper()
	for end := time.Now().Add(time.Minute); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if r := g.raw(t, n); r != "" {
			return r
		}
	}
	t.Fatalf("no reply with the id %d within 60 s", n)
	return ""
}

// wait waits up to limit until every started request answered. It reports
// whether they all did.
func (g *gitRequests) wait(limit time.Duration) bool {
	done := make(chan struct{})
	go func() {
		g.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(limit):
		return false
	}
}
