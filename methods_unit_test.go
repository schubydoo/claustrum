package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/schubydoo/claustrum/internal/homeguard"
)

// rpcLine builds a single authenticated JSON-RPC request line. params is
// marshaled as-is (pass a map or struct); a nil params is sent as `{}` so the
// needParams presence gate is satisfied.
func rpcLine(t *testing.T, method string, params interface{}) string {
	t.Helper()
	if params == nil {
		params = struct{}{}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	req := request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  method,
		Params:  raw,
		Auth:    testToken,
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return string(b)
}

// homeguard.IsFilesystemRoot must recognise the platform's OWN root, not just "/".
//
// The gate it backs guards the wipe of destDir, a recursive delete. The previous spelling compared
// against the literal "/", which a Windows volume root never equals — `C:\`
// cleans to itself and passes filepath.IsAbs, so it reached the wipe.
//
// The Windows rows are the point of this test and can only fail on the Windows
// leg; the Unix rows keep it honest there.
func TestIsFilesystemRoot(t *testing.T) {
	var roots, nonRoots []string
	if runtime.GOOS == "windows" {
		roots = []string{`C:\`, `C:\\`, `\\srv\share`}
		nonRoots = []string{`C:\tmp`, `C:\tmp\out`, `\\srv\share\sub`}
	} else {
		roots = []string{"/", "//", "/."}
		nonRoots = []string{"/tmp", "/tmp/out", "/tmp/"}
	}
	for _, r := range roots {
		if !homeguard.IsFilesystemRoot(r) {
			t.Errorf("homeguard.IsFilesystemRoot(%q) = false, want true — a root destDir reaches the wipe", r)
		}
	}
	for _, n := range nonRoots {
		if homeguard.IsFilesystemRoot(n) {
			t.Errorf("homeguard.IsFilesystemRoot(%q) = true, want false — this would refuse a legitimate destDir", n)
		}
	}
}

// On a non-repo path, git.info returns the bare {isRepo:false} and
// git.list_branches its full branches:[] shape, matching the reference. Since
// 7d193f89 git.status instead REQUIRES baseRepo, so a path-only call rejects with
// -32602 before any repo check. git.worktree_create reports a clean not_a_repo
// error instead of leaking git's raw "not a git repository" output.
func TestGitNonRepoResults(t *testing.T) {
	s := newTestServer(t)
	dir := t.TempDir() // under /tmp — not a git repo

	for _, tc := range []struct{ method, wantResult string }{
		// git.info gained repoSlug/defaultBranch in 7c2f88d — always present, empty
		// for a non-repo. git.list_branches keeps its own non-repo shape; git.status
		// now demands baseRepo first.
		{"git.info", `"result":{"isRepo":false,"repoSlug":"","defaultBranch":""}}`},
		{"git.status", `"error":{"code":-32602,"message":"baseRepo is required"}`},
		{"git.list_branches", `"result":{"isRepo":false,"branches":[]}}`},
	} {
		got := dispatchRaw(t, s, rpcLine(t, tc.method, map[string]any{"path": dir}))
		if !strings.Contains(got, tc.wantResult) {
			t.Errorf("%s on non-repo = %s, want result %s", tc.method, got, tc.wantResult)
		}
	}

	got := dispatchRaw(t, s, rpcLine(t, "git.worktree_create", map[string]any{
		"baseRepo": dir, "branchName": "b", "worktreePath": filepath.Join(dir, "wt"),
	}))
	if !strings.Contains(got, `"success":false`) ||
		!strings.Contains(got, `"error":"not a git repository"`) ||
		!strings.Contains(got, `"errorCode":"not_a_repo"`) {
		t.Errorf("worktree_create on non-repo = %s, want not_a_repo error", got)
	}
}

// git.info resolves the branch via branch --show-current, which works on an unborn HEAD
// (empty repo → the init branch name) and lets a detached HEAD be reported as
// "detached:<short-sha>" — where `rev-parse --abbrev-ref HEAD` would fail (and
// previously leaked git's error text) or return "HEAD".
func TestGitInfoBranchEdges(t *testing.T) {
	requireGit(t)
	s := newTestServer(t)

	// Empty repo (no commits): the init branch name still resolves.
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, empty, "init", "-b", "trunk")
	if got := dispatchRaw(t, s, rpcLine(t, "git.info", map[string]any{"path": empty})); !strings.Contains(got, `"branch":"trunk"`) {
		t.Errorf("empty-repo git.info = %s, want branch=trunk", got)
	}

	// Detached HEAD → "detached:<short-sha>".
	det := filepath.Join(t.TempDir(), "det")
	if err := os.MkdirAll(det, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, det, "init", "-b", "trunk")
	writeFile(t, filepath.Join(det, "a.txt"), "x", 0o644)
	runGit(t, det, "add", "-A")
	runGit(t, det, "commit", "-m", "init")
	out, err := exec.Command("git", "-C", det, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.TrimSpace(string(out))
	runGit(t, det, "-c", "advice.detachedHead=false", "checkout", sha)
	if got := dispatchRaw(t, s, rpcLine(t, "git.info", map[string]any{"path": det})); !strings.Contains(got, `"branch":"detached:`+sha+`"`) {
		t.Errorf("detached git.info = %s, want branch=detached:%s", got, sha)
	}
}

// worktree_create off an empty (unborn-HEAD) repo FAILS at 7d193f89 (it succeeded
// with an orphan branch at 5db5e4a). The two-step create passes --no-track, and on
// an unborn HEAD git infers --orphan, which cannot be combined with --track — so git
// worktree add fails and the daemon surfaces that. Measured byte-for-byte against the
// pinned 7d193f89 reference. Git 2.47 starts that text with "options " and git 2.43
// does not, so the test asks for the part that both print.
func TestGitWorktreeCreateEmptyRepo(t *testing.T) {
	requireGit(t)
	s := newTestServer(t)
	base := filepath.Join(t.TempDir(), "empty")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, base, "init", "-b", "trunk")
	wt := filepath.Join(base, ".claude", "worktrees", "wt") // 7d193f89: inside the repo
	got := dispatchRaw(t, s, rpcLine(t, "git.worktree_create", map[string]any{
		"baseRepo": base, "branchName": "newbr", "worktreePath": wt,
	}))
	if !strings.Contains(got, `"errorCode":"worktree_add_failed"`) ||
		!strings.Contains(got, `'--orphan' and '--track' cannot be used together`) {
		t.Errorf("worktree off empty repo = %s, want the --orphan/--track failure", got)
	}
}

// git.worktree_create accepts a caller-supplied timeoutMs (4534d86): absent is
// byte-identical to the pre-4534d86 success reply, and a fired deadline reports
// errorCode "timeout" rather than worktree_add_failed. A 1ms deadline is below
// the cost of spawning git at all, so it fires deterministically on every OS.
func TestGitWorktreeCreateTimeoutMs(t *testing.T) {
	requireGit(t)
	s := newTestServer(t)
	base := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, base, "init", "-b", "trunk")
	runGit(t, base, "-c", "user.email=a@b.c", "-c", "user.name=t", "commit", "--allow-empty", "-m", "init")

	// Absent timeoutMs: succeeds, byte-identical to the pre-4534d86 reply.
	wt := filepath.Join(base, ".claude", "worktrees", "ok")
	got := dispatchRaw(t, s, rpcLine(t, "git.worktree_create", map[string]any{
		"baseRepo": base, "branchName": "b1", "worktreePath": wt,
	}))
	if !strings.Contains(got, `"success":true`) {
		t.Fatalf("worktree_create without timeoutMs = %s, want success", got)
	}

	// A 1ms deadline fires before git can finish, yielding errorCode "timeout".
	wt2 := filepath.Join(base, ".claude", "worktrees", "slow")
	got = dispatchRaw(t, s, rpcLine(t, "git.worktree_create", map[string]any{
		"baseRepo": base, "branchName": "b2", "worktreePath": wt2, "timeoutMs": 1,
	}))
	if !strings.Contains(got, `"errorCode":"timeout"`) ||
		!strings.Contains(got, "git worktree add timed out after 1ms") {
		t.Errorf("worktree_create timeoutMs=1 = %s, want errorCode timeout", got)
	}
}

// The procManager mutators must be no-ops (not panics) for unknown ids and
// procs that never got a running command/stdin.
func TestProcManagerEdgeCases(t *testing.T) {
	m := newTestProcManager(t)

	if m.writeStdin("missing", []byte("x")) {
		t.Error("writeStdin(missing) = true, want false")
	}
	// A proc with no stdin pipe rejects writes without panicking.
	m.procs["nostdin"] = &managedProc{id: "nostdin", subs: map[*conn]struct{}{}}
	if m.writeStdin("nostdin", []byte("x")) {
		t.Error("writeStdin(no stdin) = true, want false")
	}

	// kill on an unknown id and on a proc with no started process are both no-ops.
	_ = m.kill("missing", "TERM")
	_ = m.kill("nostdin", "KILL")

	// killAll over procs that never started must not panic either.
	m.killAll()
}

// detachConn unsubscribes a conn from every managed proc's replay fan-out.
func TestProcManagerDetachConn(t *testing.T) {
	m := newTestProcManager(t)
	c, _ := pipeConn(t)
	p := &managedProc{id: "p", subs: map[*conn]struct{}{c: {}}}
	m.procs["p"] = p

	m.detachConn(c)
	p.mu.Lock()
	_, still := p.subs[c]
	p.mu.Unlock()
	if still {
		t.Error("detachConn left the conn subscribed")
	}
}

func TestProcessSpawnValidation(t *testing.T) {
	s := newTestServer(t)

	got := dispatchRaw(t, s, rpcLine(t, "process.spawn", map[string]any{"command": "echo"}))
	if !strings.Contains(got, "Process ID is required") {
		t.Errorf("spawn without id = %s, want id-required error", got)
	}
	got = dispatchRaw(t, s, rpcLine(t, "process.spawn", map[string]any{"id": "p1"}))
	if !strings.Contains(got, "Command is required") {
		t.Errorf("spawn without command = %s, want command-required error", got)
	}
}

func TestProcessStdinErrors(t *testing.T) {
	s := newTestServer(t)
	goodB64 := base64.StdEncoding.EncodeToString([]byte("x"))

	// Unknown process id (valid payload) → Process not found.
	got := dispatchRaw(t, s, rpcLine(t, "process.stdin", map[string]any{"id": "missing", "data": goodB64}))
	if !strings.Contains(got, "Process not found") {
		t.Errorf("stdin to missing = %s, want not-found error", got)
	}

	// Invalid base64 is rejected BEFORE the process lookup (probe-verified
	// precedence): even an unknown id reports the decode error, not not-found.
	got = dispatchRaw(t, s, rpcLine(t, "process.stdin", map[string]any{"id": "missing", "data": "!!!not base64!!!"}))
	if !strings.Contains(got, "Invalid base64 data") {
		t.Errorf("stdin bad base64 (unknown id) = %s, want Invalid base64 data", got)
	}

	// Registered but NOT running (running:false) → Process not running.
	s.procs.procs["p1"] = &managedProc{id: "p1", subs: map[*conn]struct{}{}, running: false}
	got = dispatchRaw(t, s, rpcLine(t, "process.stdin", map[string]any{"id": "p1", "data": goodB64}))
	if !strings.Contains(got, "Process not running") {
		t.Errorf("stdin to exited = %s, want not-running error", got)
	}
	// ...and bad base64 still wins over the running check (decode is first).
	got = dispatchRaw(t, s, rpcLine(t, "process.stdin", map[string]any{"id": "p1", "data": "!!!not base64!!!"}))
	if !strings.Contains(got, "Invalid base64 data") {
		t.Errorf("stdin bad base64 (not-running id) = %s, want Invalid base64 data", got)
	}
}

func TestProcessReattachValidation(t *testing.T) {
	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "process.reattach", map[string]any{"fromSeq": 0}))
	if !strings.Contains(got, "Process ID is required") {
		t.Errorf("reattach without id = %s, want id-required error", got)
	}
}

// Each namespace handler routes an unrecognized method to the shared
// "Unknown method" error (its switch default).
func TestRoutingUnknownMethodPerNamespace(t *testing.T) {
	s := newTestServer(t)
	for _, m := range []string{"files.bogus", "git.bogus", "process.bogus"} {
		got := dispatchRaw(t, s, rpcLine(t, m, map[string]any{"x": 1}))
		if want := "Unknown method: " + m; !strings.Contains(got, want) {
			t.Errorf("dispatch %s = %s, want %q", m, got, want)
		}
	}
}

// Each namespace handler rejects a known method that arrives with no params
// object via the shared needParams gate. The reply carries the id of the
// request.
func TestRoutingMissingParamsPerNamespace(t *testing.T) {
	s := newTestServer(t)
	auth := `"auth":"` + testToken + `"`
	for _, m := range []string{"files.read", "git.info", "process.spawn"} {
		line := `{"jsonrpc":"2.0","id":7,"method":"` + m + `",` + auth + `}`
		got := dispatchRaw(t, s, line)
		if !strings.Contains(got, "Invalid params") {
			t.Errorf("dispatch %s without params = %s, want Invalid params", m, got)
		}
		if !strings.Contains(got, `"id":7,`) {
			t.Errorf("dispatch %s without params = %s, want the id 7 of the request", m, got)
		}
	}
}

// --- error-path coverage ---

// process.spawn: a JSON type mismatch in params hits the bindParams error path.
func TestProcessSpawnBindParamsError(t *testing.T) {
	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "process.spawn", map[string]any{"id": "p1", "command": 123}))
	if !strings.Contains(got, "Invalid params") {
		t.Errorf("spawn with wrong command type = %s, want Invalid params", got)
	}
}

// process.spawn: valid params but the binary doesn't exist → rpc.CodeInternal error.
func TestProcessSpawnFailed(t *testing.T) {
	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "process.spawn", map[string]any{
		"id": "p_err", "command": "/nonexistent_binary_xyz_claustrum",
	}))
	if !strings.Contains(got, `"error"`) {
		t.Errorf("spawn nonexistent binary = %s, want error result", got)
	}
}

// process.kill: a JSON type mismatch in params hits the bindParams error path.
func TestProcessKillBindParamsError(t *testing.T) {
	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "process.kill", map[string]any{"id": "p1", "signal": 123}))
	if !strings.Contains(got, "Invalid params") {
		t.Errorf("kill with wrong signal type = %s, want Invalid params", got)
	}
}

// process.reattach: a JSON type mismatch in params hits the bindParams error path.
func TestProcessReattachBindParamsError(t *testing.T) {
	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "process.reattach", map[string]any{"id": "p1", "fromSeq": "nope"}))
	if !strings.Contains(got, "Invalid params") {
		t.Errorf("reattach with wrong fromSeq type = %s, want Invalid params", got)
	}
}

// git.worktree_create: trying to create a worktree with a branch that already
// exists causes git worktree add to fail → worktree_add_failed error code.
func TestGitWorktreeCreateFailed(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	runGit(t, root, "init", "-b", "main", "repo")
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "f")
	runGit(t, repo, "commit", "-m", "init")
	s := newTestServer(t)
	wt := filepath.Join(repo, ".claude", "worktrees", "wt")
	got := dispatchRaw(t, s, rpcLine(t, "git.worktree_create", map[string]any{
		"baseRepo": repo, "branchName": "main", "worktreePath": wt,
	}))
	if !strings.Contains(got, "worktree_add_failed") {
		t.Errorf("worktree_create with existing branch = %s, want worktree_add_failed", got)
	}
	// 4534d86 reports the git failure on a single line — it joins git's two stderr
	// lines with a space, so the wire message carries no newline (measured).
	if strings.Contains(got, `\n`) {
		t.Errorf("worktree_add_failed error carries a newline, want single-line: %s", got)
	}
	// 4534d86 rolls back the leaf claustrum pre-created, so a failed add leaves no
	// partial worktree directory behind (and a retry at the same path then works).
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("leaf %s survived a failed add (stat err=%v), want rolled back", wt, err)
	}
	// The failed add must NOT delete the pre-existing branch it collided with —
	// runGit t.Fatal's if refs/heads/main is gone.
	runGit(t, repo, "rev-parse", "--verify", "refs/heads/main")
}

// git.worktree_remove: a JSON type mismatch in params hits the bindParams error path.
func TestGitWorktreeRemoveBindParamsError(t *testing.T) {
	s := newTestServer(t)
	got := dispatchRaw(t, s, rpcLine(t, "git.worktree_remove", map[string]any{"worktreePath": 123}))
	if !strings.Contains(got, "Invalid params") {
		t.Errorf("worktree_remove wrong type = %s, want Invalid params", got)
	}
}
