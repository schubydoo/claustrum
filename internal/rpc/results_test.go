package rpc

import (
	"encoding/json"
	"testing"
)

// The wire contract is byte-exact: Go marshals struct fields in declaration
// order, and `omitempty` decides which keys appear. These tests pin the exact
// JSON of the result shapes so a field reorder or a stray/missing
// `omitempty` (which would diverge from the reference daemon) fails CI.
func TestResultMarshalingIsByteExact(t *testing.T) {
	cases := []struct {
		name string
		val  interface{}
		want string
	}{
		{"pong", PongResult{Pong: true}, `{"pong":true}`},
		{"capabilities", CapabilitiesResult{Version: "v1", Methods: []string{"server.ping"}, Features: []string{"process.stdin.offset"}, PeerCheck: "off"},
			`{"version":"v1","methods":["server.ping"],"features":["process.stdin.offset"],"peerCheck":"off"}`},
		// 4534d86: instanceId + startedAt sit between methods and features.
		{"capabilities full", CapabilitiesResult{Version: "v1", Methods: []string{"server.ping"}, InstanceID: "0123456789abcdef0123456789abcdef", StartedAt: 1700000000000, Features: []string{"process.stdin.offset", "server.instance_id"}, PeerCheck: "unavailable"},
			`{"version":"v1","methods":["server.ping"],"instanceId":"0123456789abcdef0123456789abcdef","startedAt":1700000000000,"features":["process.stdin.offset","server.instance_id"],"peerCheck":"unavailable"}`},
		// peerCheck follows features, and a zero value stays.
		{"capabilities zero peerCheck", CapabilitiesResult{Version: "v1", Methods: []string{"server.ping"}, Features: []string{}},
			`{"version":"v1","methods":["server.ping"],"features":[],"peerCheck":""}`},

		{"stat zero", StatResult{}, `{"exists":false,"isDir":false,"size":0,"mode":""}`},
		{"stat full", StatResult{Exists: true, IsDir: true, Size: 42, Mode: "drwxr-xr-x"},
			`{"exists":true,"isDir":true,"size":42,"mode":"drwxr-xr-x"}`},

		{"list empty", ListResult{Entries: []ListEntry{}}, `{"entries":[]}`},
		{"list one", ListResult{Entries: []ListEntry{{Name: "a", Path: "/x/a", IsDir: false}}},
			`{"entries":[{"name":"a","path":"/x/a","isDir":false}]}`},

		{"read miss", ReadResult{}, `{"content":"","exists":false}`},
		{"read hit", ReadResult{Content: "hi", Exists: true}, `{"content":"hi","exists":true}`},

		// validate: the `error` key must vanish on success.
		{"validate ok", ValidateResult{Valid: true, IsDir: true}, `{"valid":true,"isDir":true}`},
		{"validate miss", ValidateResult{Error: "Path does not exist"},
			`{"valid":false,"isDir":false,"error":"Path does not exist"}`},

		{"extract ok", ExtractResult{Success: true, FileCount: 3}, `{"success":true,"fileCount":3}`},
		{"extract err", ExtractResult{FileCount: 1, Error: "gzip: bad"},
			`{"success":false,"fileCount":1,"error":"gzip: bad"}`},

		{"success", SuccessResult{Success: true}, `{"success":true}`},
		{"notRepo", NotRepoResult{}, `{"isRepo":false,"repoSlug":"","defaultBranch":""}`},

		{"gitInfo", GitInfoResult{IsRepo: true, Repo: "claustrum", Branch: "main", Root: "/src/claustrum"},
			`{"isRepo":true,"repo":"claustrum","branch":"main","root":"/src/claustrum","repoSlug":"","defaultBranch":""}`},
		{"gitInfo slug", GitInfoResult{IsRepo: true, Repo: "claustrum", Branch: "main", Root: "/src/claustrum", RepoSlug: "acme/claustrum", DefaultBranch: "main"},
			`{"isRepo":true,"repo":"claustrum","branch":"main","root":"/src/claustrum","repoSlug":"acme/claustrum","defaultBranch":"main"}`},
		{"status clean", GitStatusResult{IsRepo: true, Clean: true}, `{"isRepo":true,"clean":true}`},
		{"status dirty", GitStatusResult{IsRepo: true, Changes: []string{"M a"}},
			`{"isRepo":true,"clean":false,"changes":["M a"]}`},

		// branches is NOT omitempty: an empty list serializes as [].
		{"branches empty", BranchesResult{IsRepo: true, Branches: []string{}},
			`{"isRepo":true,"branches":[]}`},

		{"worktree ok", WorktreeResult{Success: true, Path: "/wt", SourceBranch: "main"},
			`{"success":true,"path":"/wt","sourceBranch":"main"}`},
		{"worktree err", WorktreeResult{Error: "git worktree add failed: x", ErrorCode: "worktree_add_failed"},
			`{"success":false,"error":"git worktree add failed: x","errorCode":"worktree_add_failed"}`},

		{"reattach", ReattachResult{Found: true, Running: true, FirstSeq: 1, LastSeq: 5},
			`{"found":true,"running":true,"firstSeq":1,"lastSeq":5,"stdinApplied":0}`},
		{"reattach stdin", ReattachResult{Found: true, Running: true, FirstSeq: 1, LastSeq: 5, StdinApplied: 12},
			`{"found":true,"running":true,"firstSeq":1,"lastSeq":5,"stdinApplied":12}`},

		// stdin: applied is never omitempty (emitted even at 0); duplicate drops when false.
		{"stdin plain", StdinResult{Success: true, Applied: 6}, `{"success":true,"applied":6}`},
		{"stdin dup", StdinResult{Success: true, Applied: 12, Duplicate: true},
			`{"success":true,"applied":12,"duplicate":true}`},

		// killAndWait: alreadyExited/escalated drop when false.
		{"killwait plain", KillAndWaitResult{Found: true, Died: true}, `{"found":true,"died":true}`},
		{"killwait unknown", KillAndWaitResult{}, `{"found":false,"died":false}`},
		{"killwait already", KillAndWaitResult{Found: true, Died: true, AlreadyExited: true},
			`{"found":true,"died":true,"alreadyExited":true}`},
		{"killwait escalated", KillAndWaitResult{Found: true, Died: true, Escalated: true},
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

// A successful response omits `error`; an error response omits `result`. Both
// keep the jsonrpc/id ordering the reference daemon emits.
func TestResponseEnvelopeMarshaling(t *testing.T) {
	ok := OKResult(json.RawMessage(`7`), PongResult{Pong: true})
	if b, _ := json.Marshal(ok); string(b) != `{"jsonrpc":"2.0","id":7,"result":{"pong":true}}` {
		t.Errorf("ok envelope: %s", b)
	}
	e := ErrResult(json.RawMessage(`"abc"`), CodeMethod, "Unknown method: x")
	if b, _ := json.Marshal(e); string(b) != `{"jsonrpc":"2.0","id":"abc","error":{"code":-32601,"message":"Unknown method: x"}}` {
		t.Errorf("err envelope: %s", b)
	}
}
