package main

import (
	"encoding/base64"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// These tests pin the wire surface added by the reference daemon in 7c2f88d:
// process.killAndWait, the process.stdin.offset idempotency contract (stdin
// "applied"/"duplicate" + reattach "stdinApplied"), git.info repoSlug/
// defaultBranch, and the server.capabilities "features" array. See
// docs/PROTOCOL.md and docs/UPSTREAM-TRACKING.md.

// rpcEnvelope is a minimal reply decoder for the tests below.
type rpcEnvelope struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeReply(t *testing.T, raw []byte, out any) *rpcEnvelope {
	t.Helper()
	var env rpcEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal reply %s: %v", raw, err)
	}
	if out != nil && env.Result != nil {
		if err := json.Unmarshal(env.Result, out); err != nil {
			t.Fatalf("unmarshal result %s: %v", env.Result, err)
		}
	}
	return &env
}

// spawnReqArgs builds a process.spawn request line (auth embedded) for the given
// helper mode with explicit args — spawnReq hardcodes an empty arg list.
func spawnReqArgs(t *testing.T, id int, procID, mode string, args ...string) string {
	t.Helper()
	exe, env := helperCommand(t, mode)
	if args == nil {
		args = []string{}
	}
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "process.spawn", "auth": testToken,
		"params": map[string]any{"id": procID, "command": exe, "args": args, "env": env},
	})
	if err != nil {
		t.Fatalf("marshal spawn request: %v", err)
	}
	return string(b)
}

// parseRepoSlug reduces a remote URL to owner/repo only when the path after the
// host is exactly two segments — a probe-verified quirk of the reference.
func TestParseRepoSlug(t *testing.T) {
	cases := []struct{ url, want string }{
		{"git@github.com:acme/widgets.git", "acme/widgets"},
		{"https://github.com/acme/widgets.git", "acme/widgets"},
		{"https://github.com/acme/nosuffix", "acme/nosuffix"},
		{"ssh://git@github.com/acme/proj.git", "acme/proj"},
		{"https://user:pass@github.com/acme/proj.git", "acme/proj"},
		{"https://github.com/acme/proj/", "acme/proj"},
		{"git@github.com:acme/proj", "acme/proj"},
		{"https://github.com/Acme-Org/My_Repo.git", "Acme-Org/My_Repo"},
		{"https://github.com/acme/my-repo.git", "acme/my-repo"},
		{"https://github.com/acme/my.repo.git", "acme/my.repo"},
		// Three-segment paths (GitLab subgroups) yield "" — the reference requires
		// exactly two segments, it does not take the last two.
		{"https://gitlab.com/group/sub/proj.git", ""},
		{"", ""},
		{"not a url", ""},
	}
	for _, tc := range cases {
		if got := parseRepoSlug(tc.url); got != tc.want {
			t.Errorf("parseRepoSlug(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

// git.info populates repoSlug from remote.origin.url and defaultBranch from
// refs/remotes/origin/HEAD; both are empty (but present) when unset.
func TestGitInfoRepoSlugAndDefaultBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(cmd.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	run("commit", "-q", "--allow-empty", "-m", "init")
	run("remote", "add", "origin", "git@github.com:acme/gizmo.git")
	head, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	run("update-ref", "refs/remotes/origin/main", strings.TrimSpace(string(head)))
	run("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

	s := newTestServer()
	raw := dispatchRaw(t, s, rpcLine(t, "git.info", map[string]any{"path": dir}))
	var got gitInfoResult
	decodeReply(t, []byte(raw), &got)
	if got.RepoSlug != "acme/gizmo" {
		t.Errorf("repoSlug = %q, want acme/gizmo", got.RepoSlug)
	}
	if got.DefaultBranch != "main" {
		t.Errorf("defaultBranch = %q, want main", got.DefaultBranch)
	}
}

// server.capabilities advertises process.killAndWait (between kill and reattach)
// and the process.stdin.offset feature.
func TestCapabilitiesAdvertisesNewSurface(t *testing.T) {
	s := newTestServer()
	raw := dispatchRaw(t, s, rpcLine(t, "server.capabilities", map[string]any{}))
	var got capabilitiesResult
	decodeReply(t, []byte(raw), &got)

	joined := strings.Join(got.Methods, ",")
	if !strings.Contains(joined, "process.kill,process.killAndWait,process.reattach") {
		t.Errorf("methods missing killAndWait in the right slot: %v", got.Methods)
	}
	if len(got.Features) != 1 || got.Features[0] != "process.stdin.offset" {
		t.Errorf("features = %v, want [process.stdin.offset]", got.Features)
	}
}

// The process.stdin.offset contract: applied advances by fresh bytes; a wholly
// covered write is a no-op flagged duplicate; a partial overlap applies only the
// fresh tail; an offset ahead of applied is a gap error; reattach reports the
// cumulative stdinApplied. Only the fresh bytes reach the child (cat).
func TestSocketProcessStdinOffset(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	cl.send(spawnReq(t, 1, "CAT", "cat"))
	cl.waitResponses(1)

	stdin := func(id int, data string, offset *int) rpcEnvelope {
		p := map[string]any{"id": "CAT", "data": base64.StdEncoding.EncodeToString([]byte(data))}
		if offset != nil {
			p["offset"] = *offset
		}
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "process.stdin", "params": p})
		raw := cl.call(authed(string(body)))
		var sr stdinResult
		env := decodeReply(t, raw, &sr)
		if env.Error == nil {
			env.Result, _ = json.Marshal(sr) // canonicalize field order for string compare
		}
		return *env
	}
	off := func(n int) *int { return &n }

	if env := stdin(2, "AAA\n", nil); string(env.Result) != `{"success":true,"applied":4}` {
		t.Errorf("stdin no-offset result = %s", env.Result)
	}
	if env := stdin(3, "BBB\n", off(4)); string(env.Result) != `{"success":true,"applied":8}` {
		t.Errorf("stdin offset==applied result = %s", env.Result)
	}
	if env := stdin(4, "DUP\n", off(4)); string(env.Result) != `{"success":true,"applied":8,"duplicate":true}` {
		t.Errorf("stdin duplicate result = %s", env.Result)
	}
	if env := stdin(5, "PART\n", off(6)); string(env.Result) != `{"success":true,"applied":11}` {
		t.Errorf("stdin partial result = %s", env.Result)
	}
	if env := stdin(6, "GAP\n", off(99)); env.Error == nil || env.Error.Code != codeStdinOffsetGap {
		t.Errorf("stdin gap = %+v, want code %d", env.Error, codeStdinOffsetGap)
	}

	raw := cl.call(authed(`{"jsonrpc":"2.0","id":7,"method":"process.reattach","params":{"id":"CAT","fromSeq":0}}`))
	var ra reattachResult
	decodeReply(t, raw, &ra)
	if ra.StdinApplied != 11 {
		t.Errorf("reattach stdinApplied = %d, want 11", ra.StdinApplied)
	}

	// Only the fresh bytes were echoed by cat: AAA + BBB + RT (never DUP/PART-head/GAP).
	cl.wait(func() bool {
		var got []byte
		for _, f := range cl.fr {
			if f.ProcessID == "CAT" && f.Stream == "stdout" {
				b, _ := base64.StdEncoding.DecodeString(f.Data)
				got = append(got, b...)
			}
		}
		return string(got) == "AAA\nBBB\nRT\n"
	})
	cl.send(authed(`{"jsonrpc":"2.0","id":8,"method":"process.kill","params":{"id":"CAT","signal":"KILL"}}`))
	cl.waitExit("CAT")
}

// process.killAndWait: missing id is an error; an unknown id is a non-error
// {found:false,died:false}; an already-exited process reports alreadyExited; a
// live process is signalled and reported died (no escalation for a cooperative
// child).
func TestSocketProcessKillAndWait(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)

	if env := decodeReply(t, cl.call(authed(`{"jsonrpc":"2.0","id":1,"method":"process.killAndWait","params":{}}`)), nil); env.Error == nil {
		t.Error("killAndWait without id = no error, want Process ID is required")
	}

	var kw killAndWaitResult
	decodeReply(t, cl.call(authed(`{"jsonrpc":"2.0","id":2,"method":"process.killAndWait","params":{"id":"ghost"}}`)), &kw)
	if kw.Found || kw.Died {
		t.Errorf("killAndWait unknown = %+v, want found:false died:false", kw)
	}

	// already-exited: echo exits immediately; wait for its exit, then killAndWait.
	cl.send(spawnReq(t, 3, "Q", "echo"))
	cl.waitExit("Q")
	kw = killAndWaitResult{}
	decodeReply(t, cl.call(authed(`{"jsonrpc":"2.0","id":4,"method":"process.killAndWait","params":{"id":"Q"}}`)), &kw)
	if !kw.Found || !kw.Died || !kw.AlreadyExited {
		t.Errorf("killAndWait already-exited = %+v, want found&died&alreadyExited", kw)
	}

	// live process: the sleep helper responds to the default SIGTERM and dies well
	// within the grace window, so no escalation. call() waits for the spawn reply
	// before we kill, so the process is registered.
	cl.call(spawnReqArgs(t, 5, "SL", "sleep", "30"))
	kw = killAndWaitResult{}
	decodeReply(t, cl.call(authed(`{"jsonrpc":"2.0","id":6,"method":"process.killAndWait","params":{"id":"SL"}}`)), &kw)
	if !kw.Found || !kw.Died || kw.AlreadyExited || kw.Escalated {
		t.Errorf("killAndWait live = %+v, want found&died only", kw)
	}
}
