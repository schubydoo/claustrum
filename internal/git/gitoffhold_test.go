package git

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// offHoldFixture is the git state of a daemon in the state shadow with the
// stub git of newSeededShadowFixture. Each request runs in a goroutine of its
// own on that one state. Repository A is the repository of that fixture, and B
// is a second repository. The stub holds the first call that match names until
// release runs.
type offHoldFixture struct {
	shadowFixture
	g     *gitRequests
	repoB string
	gate  string
}

func newOffHoldFixture(t *testing.T, match string) offHoldFixture {
	t.Helper()
	f := newSeededShadowFixture(t)
	repoB, _ := configRepo(t)
	dir := t.TempDir()
	gate := filepath.Join(dir, "gate")
	slowGit(t, match, "gate", 10*time.Minute, "", "")
	t.Setenv("CLAUSTRUM_GITSTUB_GATE", gate)
	t.Setenv("CLAUSTRUM_GITSTUB_NTHFILE", filepath.Join(dir, "nth"))
	t.Setenv("CLAUSTRUM_GITSTUB_NTH", "1")
	h := offHoldFixture{shadowFixture: f, g: newGitRequests(f.d), repoB: repoB, gate: gate}
	// The cleanup opens the gate and waits for every reply, so that no git call
	// runs while t.TempDir removes the repositories.
	t.Cleanup(func() {
		h.release()
		if !h.g.wait(time.Minute) {
			t.Error("a request did not answer within 60 s of the release")
		}
	})
	return h
}

// send starts one request with the id n.
func (h offHoldFixture) send(t *testing.T, n int, method string, params map[string]any) {
	t.Helper()
	h.g.start(n, rpcLine(t, method, params))
}

// reply waits up to 60 s for the reply with the id n and returns its result.
func (h offHoldFixture) reply(t *testing.T, n int) json.RawMessage {
	t.Helper()
	line := h.g.reply(t, n)
	var r struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &r); err != nil {
		t.Fatal(err)
	}
	if r.Result == nil {
		t.Fatalf("reply %d = %s, want a result", n, line)
	}
	return r.Result
}

// replies is the count of replies so far.
func (h offHoldFixture) replies() int {
	return h.g.count()
}

// held waits up to 60 s until the stub holds the call.
func (h offHoldFixture) held(t *testing.T) {
	t.Helper()
	for end := time.Now().Add(time.Minute); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(h.gate + ".in"); err == nil {
			return
		}
	}
	t.Fatal("the stub held no call within 60 s")
}

func (h offHoldFixture) release() {
	_ = os.WriteFile(h.gate, nil, 0o644)
}

// settingsControl is the member settingsControl of a git.info result.
func settingsControlOf(t *testing.T, result json.RawMessage) string {
	t.Helper()
	var r struct {
		SettingsControl string `json:"settingsControl"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		t.Fatal(err)
	}
	return r.SettingsControl
}

// While a git.info with "shadowDirOff":true on repository A runs, a git.info on
// repository B without the member answers "settingsControl":"off-switch"
// (measured on Linux). After the first request answered, the next git.info on B
// answers "shadow" again (measured). The reply reads the hold when it is made
// (measured). Mutation: read req.offOwn in place of req.offLive() at the reply of
// gitInfo.
func TestOffSwitchHoldsTheDaemonWhileARequestRuns(t *testing.T) {
	h := newOffHoldFixture(t, "remote,get-url")
	h.send(t, 1, "git.info", map[string]any{"path": h.repo, "shadowDirOff": true})
	h.held(t)
	h.send(t, 2, "git.info", map[string]any{"path": h.repoB})
	if got := settingsControlOf(t, h.reply(t, 2)); got != controlOff {
		t.Errorf("H1: settingsControl of B = %q, want %q", got, controlOff)
	}
	if n := h.replies(); n != 1 {
		t.Fatalf("%d replies before the release, want 1: the request on A did not hold", n)
	}
	h.release()
	if got := settingsControlOf(t, h.reply(t, 1)); got != controlOff {
		t.Errorf("settingsControl of A = %q, want %q", got, controlOff)
	}
	h.send(t, 3, "git.info", map[string]any{"path": h.repoB})
	if got := settingsControlOf(t, h.reply(t, 3)); got != controlShadow {
		t.Errorf("H2: settingsControl of B after the reply of A = %q, want %q", got, controlShadow)
	}
	if n := h.d.OffRequests.Load(); n != 0 {
		t.Errorf("%d requests with the member after the replies, want 0", n)
	}
}

// While a git.status with "shadowDirOff":true runs, a git.worktree_create on
// another repository without the member runs as one with it. 5fd08069 answers
// success on both, and no call of the create names a copy (measured on Linux).
// The hold stays live for the whole create here, so each command of it has no
// copy (gitoffform_test.go has the changes of the hold). Mutation: set
// p.ShadowDirOff from req.offOwn alone in bindGitParams.
func TestOffSwitchHoldCoversACreate(t *testing.T) {
	h := newOffHoldFixture(t, "status,--porcelain")
	h.send(t, 1, "git.status", map[string]any{"path": h.wt, "baseRepo": h.repo, "shadowDirOff": true})
	h.held(t)
	leaf := filepath.Join(h.repoB, ".claude", "worktrees", "w1")
	h.send(t, 2, "git.worktree_create", map[string]any{"baseRepo": h.repoB, "branchName": "w1", "worktreePath": leaf})
	want := `{"success":true,"path":` + jsonQuoted(t, leaf) + `,"sourceBranch":"main","branch":"w1"}`
	if got := string(h.reply(t, 2)); got != want {
		t.Errorf("H4: create = %s\n  want %s", got, want)
	}
	if n := h.replies(); n != 1 {
		t.Fatalf("%d replies before the release, want 1: the status did not hold", n)
	}
	for _, c := range readEnvCalls(t, h.log) {
		if v, _ := lookupExact(c.env, "GIT_COMMON_DIR"); strings.Contains(v, shadowRootName) {
			t.Errorf("H4: the call %q names the copy %s, want no copy", strings.Join(c.argv, " "), v)
		}
	}
	h.release()
	if r := string(h.reply(t, 1)); !strings.Contains(r, `"isRepo":true`) {
		t.Errorf("git.status = %s, want a repository", r)
	}
}
