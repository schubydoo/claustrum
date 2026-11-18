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
		{"pong", PongReply{Pong: true}, `{"pong":true}`},
		{"capabilities", CapabilitiesReply{Version: "v1", Methods: []string{"server.ping"}, Features: []string{"process.stdin.offset"}, PeerCheck: "off"},
			`{"version":"v1","methods":["server.ping"],"features":["process.stdin.offset"],"peerCheck":"off"}`},
		// 4534d86: instanceId + startedAt sit between methods and features.
		{"capabilities full", CapabilitiesReply{Version: "v1", Methods: []string{"server.ping"}, InstanceID: "0123456789abcdef0123456789abcdef", StartedAt: 1700000000000, Features: []string{"process.stdin.offset", "server.instance_id"}, PeerCheck: "unavailable"},
			`{"version":"v1","methods":["server.ping"],"instanceId":"0123456789abcdef0123456789abcdef","startedAt":1700000000000,"features":["process.stdin.offset","server.instance_id"],"peerCheck":"unavailable"}`},
		// peerCheck follows features, and a zero value stays.
		{"capabilities zero peerCheck", CapabilitiesReply{Version: "v1", Methods: []string{"server.ping"}, Features: []string{}},
			`{"version":"v1","methods":["server.ping"],"features":[],"peerCheck":""}`},

		{"stat zero", StatReply{}, `{"exists":false,"isDir":false,"size":0,"mode":""}`},
		{"stat full", StatReply{Exists: true, IsDir: true, Size: 42, Mode: "drwxr-xr-x"},
			`{"exists":true,"isDir":true,"size":42,"mode":"drwxr-xr-x"}`},

		{"list empty", ListReply{Entries: []DirItem{}}, `{"entries":[]}`},
		{"list one", ListReply{Entries: []DirItem{{Name: "a", Path: "/x/a", IsDir: false}}},
			`{"entries":[{"name":"a","path":"/x/a","isDir":false}]}`},

		{"read miss", ReadReply{}, `{"content":"","exists":false}`},
		{"read hit", ReadReply{Content: "hi", Exists: true}, `{"content":"hi","exists":true}`},

		// validate: the `error` key must vanish on success.
		{"validate ok", ValidateReply{Valid: true, IsDir: true}, `{"valid":true,"isDir":true}`},
		{"validate miss", ValidateReply{Error: "Path does not exist"},
			`{"valid":false,"isDir":false,"error":"Path does not exist"}`},

		{"extract ok", ExtractReply{Success: true, FileCount: 3}, `{"success":true,"fileCount":3}`},
		{"extract err", ExtractReply{FileCount: 1, Error: "gzip: bad"},
			`{"success":false,"fileCount":1,"error":"gzip: bad"}`},

		{"success", SuccessReply{Success: true}, `{"success":true}`},
		{"notRepo", NotRepoReply{}, `{"isRepo":false,"repoSlug":"","defaultBranch":""}`},

		{"gitInfo", GitInfoReply{IsRepo: true, Repo: "claustrum", Branch: "main", Root: "/src/claustrum"},
			`{"isRepo":true,"repo":"claustrum","branch":"main","root":"/src/claustrum","repoSlug":"","defaultBranch":""}`},
		{"gitInfo slug", GitInfoReply{IsRepo: true, Repo: "claustrum", Branch: "main", Root: "/src/claustrum", RepoSlug: "acme/claustrum", DefaultBranch: "main"},
			`{"isRepo":true,"repo":"claustrum","branch":"main","root":"/src/claustrum","repoSlug":"acme/claustrum","defaultBranch":"main"}`},
		{"status clean", GitStatusReply{IsRepo: true, Clean: true}, `{"isRepo":true,"clean":true}`},
		{"status dirty", GitStatusReply{IsRepo: true, Changes: []string{"M a"}},
			`{"isRepo":true,"clean":false,"changes":["M a"]}`},

		// branches is NOT omitempty: an empty list serializes as [].
		{"branches empty", BranchesReply{IsRepo: true, Branches: []string{}},
			`{"isRepo":true,"branches":[]}`},

		{"worktree ok", WorktreeReply{Success: true, Path: "/wt", SourceBranch: "main"},
			`{"success":true,"path":"/wt","sourceBranch":"main"}`},
		{"worktree err", WorktreeReply{Error: "git worktree add failed: x", ErrorCode: "worktree_add_failed"},
			`{"success":false,"error":"git worktree add failed: x","errorCode":"worktree_add_failed"}`},

		{"reattach", ReattachReply{Found: true, Running: true, FirstSeq: 1, LastSeq: 5},
			`{"found":true,"running":true,"firstSeq":1,"lastSeq":5,"stdinApplied":0}`},
		{"reattach stdin", ReattachReply{Found: true, Running: true, FirstSeq: 1, LastSeq: 5, StdinApplied: 12},
			`{"found":true,"running":true,"firstSeq":1,"lastSeq":5,"stdinApplied":12}`},

		// stdin: applied is never omitempty (emitted even at 0); duplicate drops when false.
		{"stdin plain", StdinReply{Success: true, Applied: 6}, `{"success":true,"applied":6}`},
		{"stdin dup", StdinReply{Success: true, Applied: 12, Duplicate: true},
			`{"success":true,"applied":12,"duplicate":true}`},

		// killAndWait: alreadyExited/escalated drop when false.
		{"killwait plain", KillAndWaitReply{Found: true, Died: true}, `{"found":true,"died":true}`},
		{"killwait unknown", KillAndWaitReply{}, `{"found":false,"died":false}`},
		{"killwait already", KillAndWaitReply{Found: true, Died: true, AlreadyExited: true},
			`{"found":true,"died":true,"alreadyExited":true}`},
		{"killwait escalated", KillAndWaitReply{Found: true, Died: true, Escalated: true},
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
	ok := OKResult(json.RawMessage(`7`), PongReply{Pong: true})
	if b, _ := json.Marshal(ok); string(b) != `{"jsonrpc":"2.0","id":7,"result":{"pong":true}}` {
		t.Errorf("ok envelope: %s", b)
	}
	e := ErrResult(json.RawMessage(`"abc"`), CodeMethod, "Unknown method: x")
	if b, _ := json.Marshal(e); string(b) != `{"jsonrpc":"2.0","id":"abc","error":{"code":-32601,"message":"Unknown method: x"}}` {
		t.Errorf("err envelope: %s", b)
	}
}
