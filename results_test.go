package main

import (
	"encoding/json"
	"testing"
)

// The wire contract is byte-exact: Go marshals struct fields in declaration
// order, and `omitempty` decides which keys appear. These tests pin the exact
// JSON for every result/frame shape so a field reorder or a stray/missing
// `omitempty` (which would diverge from the reference daemon) fails CI.
func TestResultMarshalingIsByteExact(t *testing.T) {
	cases := []struct {
		name string
		val  interface{}
		want string
	}{
		{"pong", pongResult{Pong: true}, `{"pong":true}`},
		{"capabilities", capabilitiesResult{Version: "v1", Methods: []string{"server.ping"}, Features: []string{"process.stdin.offset"}},
			`{"version":"v1","methods":["server.ping"],"features":["process.stdin.offset"]}`},

		{"stat zero", statResult{}, `{"exists":false,"isDir":false,"size":0,"mode":""}`},
		{"stat full", statResult{Exists: true, IsDir: true, Size: 42, Mode: "drwxr-xr-x"},
			`{"exists":true,"isDir":true,"size":42,"mode":"drwxr-xr-x"}`},

		{"list empty", listResult{Entries: []listEntry{}}, `{"entries":[]}`},
		{"list one", listResult{Entries: []listEntry{{Name: "a", Path: "/x/a", IsDir: false}}},
			`{"entries":[{"name":"a","path":"/x/a","isDir":false}]}`},

		{"read miss", readResult{}, `{"content":"","exists":false}`},
		{"read hit", readResult{Content: "hi", Exists: true}, `{"content":"hi","exists":true}`},

		// validate: the `error` key must vanish on success.
		{"validate ok", validateResult{Valid: true, IsDir: true}, `{"valid":true,"isDir":true}`},
		{"validate miss", validateResult{Error: "Path does not exist"},
			`{"valid":false,"isDir":false,"error":"Path does not exist"}`},

		{"extract ok", extractResult{Success: true, FileCount: 3}, `{"success":true,"fileCount":3}`},
		{"extract err", extractResult{FileCount: 1, Error: "gzip: bad"},
			`{"success":false,"fileCount":1,"error":"gzip: bad"}`},

		{"success", successResult{Success: true}, `{"success":true}`},
		{"notRepo", notRepoResult{}, `{"isRepo":false,"repoSlug":"","defaultBranch":""}`},

		{"gitInfo", gitInfoResult{IsRepo: true, Repo: "claustrum", Branch: "main", Root: "/src/claustrum"},
			`{"isRepo":true,"repo":"claustrum","branch":"main","root":"/src/claustrum","repoSlug":"","defaultBranch":""}`},
		{"gitInfo slug", gitInfoResult{IsRepo: true, Repo: "claustrum", Branch: "main", Root: "/src/claustrum", RepoSlug: "acme/claustrum", DefaultBranch: "main"},
			`{"isRepo":true,"repo":"claustrum","branch":"main","root":"/src/claustrum","repoSlug":"acme/claustrum","defaultBranch":"main"}`},
		{"status clean", gitStatusResult{IsRepo: true, Clean: true}, `{"isRepo":true,"clean":true}`},
		{"status dirty", gitStatusResult{IsRepo: true, Changes: []string{"M a"}},
			`{"isRepo":true,"clean":false,"changes":["M a"]}`},

		// branches is NOT omitempty: an empty list serializes as [].
		{"branches empty", branchesResult{IsRepo: true, Branches: []string{}},
			`{"isRepo":true,"branches":[]}`},

		{"worktree ok", worktreeResult{Success: true, Path: "/wt", SourceBranch: "main"},
			`{"success":true,"path":"/wt","sourceBranch":"main"}`},
		{"worktree err", worktreeResult{Error: "git worktree add failed: x", ErrorCode: "worktree_add_failed"},
			`{"success":false,"error":"git worktree add failed: x","errorCode":"worktree_add_failed"}`},

		{"reattach", reattachResult{Found: true, Running: true, FirstSeq: 1, LastSeq: 5},
			`{"found":true,"running":true,"firstSeq":1,"lastSeq":5,"stdinApplied":0}`},
		{"reattach stdin", reattachResult{Found: true, Running: true, FirstSeq: 1, LastSeq: 5, StdinApplied: 12},
			`{"found":true,"running":true,"firstSeq":1,"lastSeq":5,"stdinApplied":12}`},

		// stdin: applied is never omitempty (emitted even at 0); duplicate drops when false.
		{"stdin plain", stdinResult{Success: true, Applied: 6}, `{"success":true,"applied":6}`},
		{"stdin dup", stdinResult{Success: true, Applied: 12, Duplicate: true},
			`{"success":true,"applied":12,"duplicate":true}`},

		// killAndWait: alreadyExited/escalated drop when false.
		{"killwait plain", killAndWaitResult{Found: true, Died: true}, `{"found":true,"died":true}`},
		{"killwait unknown", killAndWaitResult{}, `{"found":false,"died":false}`},
		{"killwait already", killAndWaitResult{Found: true, Died: true, AlreadyExited: true},
			`{"found":true,"died":true,"alreadyExited":true}`},
		{"killwait escalated", killAndWaitResult{Found: true, Died: true, Escalated: true},
			`{"found":true,"died":true,"escalated":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.val)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if got := string(b); got != tc.want {
				t.Errorf("\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

// streamFrame uses a pointer ExitCode so a stdout/stderr frame omits the key
// entirely while an exit frame emits exitCode:0 for a clean exit. Data is the
// mirror image (present on data frames, omitted on exit).
func TestStreamFrameMarshaling(t *testing.T) {
	zero := 0
	cases := []struct {
		name string
		f    streamFrame
		want string
	}{
		{"stdout", streamFrame{Type: "stream", ProcessID: "p1", Stream: "stdout", Seq: 1, Data: "AAA"},
			`{"type":"stream","processId":"p1","stream":"stdout","seq":1,"data":"AAA"}`},
		{"exit zero", streamFrame{Type: "stream", ProcessID: "p1", Stream: "exit", Seq: 2, ExitCode: &zero},
			`{"type":"stream","processId":"p1","stream":"exit","seq":2,"exitCode":0}`},
		// lineBytes is the replay buffer's accounting unit and must never reach
		// the wire. It is unexported today, so encoding/json drops it — but every
		// other case here leaves it at its zero value, which would keep passing if
		// someone exported it WITH omitempty. process.reattach re-marshals buffered
		// frames, and those DO carry a non-zero value, so that edit would silently
		// add a key to every replayed frame while this test stayed green.
		{"lineBytes never serialized",
			streamFrame{Type: "stream", ProcessID: "p1", Stream: "stdout", Seq: 3, Data: "AAA", lineBytes: 81},
			`{"type":"stream","processId":"p1","stream":"stdout","seq":3,"data":"AAA"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.f)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if got := string(b); got != tc.want {
				t.Errorf("\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

// A successful response omits `error`; an error response omits `result`. Both
// keep the jsonrpc/id ordering the reference daemon emits.
func TestResponseEnvelopeMarshaling(t *testing.T) {
	ok := okResult(json.RawMessage(`7`), pongResult{Pong: true})
	if b, _ := json.Marshal(ok); string(b) != `{"jsonrpc":"2.0","id":7,"result":{"pong":true}}` {
		t.Errorf("ok envelope: %s", b)
	}
	e := errResult(json.RawMessage(`"abc"`), codeMethod, "Unknown method: x")
	if b, _ := json.Marshal(e); string(b) != `{"jsonrpc":"2.0","id":"abc","error":{"code":-32601,"message":"Unknown method: x"}}` {
		t.Errorf("err envelope: %s", b)
	}
}
