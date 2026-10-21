package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// liveFixture is a socket daemon with no start test yet and the stub git of
// newShadowFixture. Repository A is the repository of that fixture, and B is a
// second repository. gate1 is the gate of the first rule of the stub, gate2 that
// of the second rule.
type liveFixture struct {
	shadowFixture
	s     *server
	cl    *testClient
	repoB string
	none  string
	gate1 string
	gate2 string
	sent  *[]int
}

func newLiveFixture(t *testing.T) liveFixture {
	t.Helper()
	f := newShadowFixture(t)
	s, sock := newRunningServer(t)
	repoB, _ := configRepo(t)
	dir := t.TempDir()
	h := liveFixture{
		shadowFixture: f, s: s, cl: dial(t, sock), repoB: repoB,
		none:  resolveTestRoot(t, t.TempDir()),
		gate1: filepath.Join(dir, "gate1"), gate2: filepath.Join(dir, "gate2"), sent: new([]int),
	}
	t.Setenv("CLAUSTRUM_GITSTUB_GATE", h.gate1)
	t.Setenv("CLAUSTRUM_GITSTUB_GATE2", h.gate2)
	t.Setenv("CLAUSTRUM_GITSTUB_NTHFILE", filepath.Join(dir, "nth"))
	t.Setenv("CLAUSTRUM_GITSTUB_NTH", "1")
	// The cleanup opens both gates and waits for every reply, so that no git
	// call runs while t.TempDir removes the repositories.
	t.Cleanup(func() {
		h.open(h.gate1)
		h.open(h.gate2)
		for _, n := range *h.sent {
			for end := time.Now().Add(time.Minute); time.Now().Before(end) && h.raw(n) == ""; time.Sleep(5 * time.Millisecond) {
			}
		}
	})
	return h
}

// send writes one request with the id n.
func (h liveFixture) send(t *testing.T, n int, method string, params map[string]any) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	*h.sent = append(*h.sent, n)
	h.cl.send(authed(`{"jsonrpc":"2.0","id":` + strconv.Itoa(n) + `,"method":"` + method + `","params":` + string(raw) + `}`))
}

// raw is the reply with the id n, or "" before it came.
func (h liveFixture) raw(n int) string {
	h.cl.mu.Lock()
	defer h.cl.mu.Unlock()
	for _, line := range h.cl.resp {
		var r struct {
			ID int `json:"id"`
		}
		if json.Unmarshal(line, &r) == nil && r.ID == n {
			return string(line)
		}
	}
	return ""
}

// reply waits up to 60 s for the reply with the id n.
func (h liveFixture) reply(t *testing.T, n int) string {
	t.Helper()
	for end := time.Now().Add(time.Minute); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if r := h.raw(n); r != "" {
			return r
		}
	}
	t.Fatalf("no reply with the id %d within 60 s", n)
	return ""
}

// waitIn waits up to 60 s until the stub holds a call at gate.
func (h liveFixture) waitIn(t *testing.T, gate string) {
	t.Helper()
	for end := time.Now().Add(time.Minute); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(gate + ".in"); err == nil {
			return
		}
	}
	t.Fatalf("the stub held no call at %s within 60 s", filepath.Base(gate))
}

func (h liveFixture) open(gate string) {
	_ = os.WriteFile(gate, nil, 0o644)
}

// holdSecond makes the second rule of the stub hold each call with the words
// of match in the folder dir until gate2 exists. With fail set, each such call
// then fails with exit code 1 and the stderr "boom".
func holdSecond(t *testing.T, match, dir string, fail bool) {
	t.Helper()
	t.Setenv("CLAUSTRUM_GITSTUB_MATCH2", match)
	t.Setenv("CLAUSTRUM_GITSTUB_DIR2", dir)
	t.Setenv("CLAUSTRUM_GITSTUB_MS2", strconv.FormatInt((10*time.Minute).Milliseconds(), 10))
	if fail {
		t.Setenv("CLAUSTRUM_GITSTUB_EXIT2", "1")
		t.Setenv("CLAUSTRUM_GITSTUB_STDERR2", "boom\n")
	}
}

// liveCommonDir is the GIT_COMMON_DIR of the first call in dir whose argv holds
// the words of match. ok is false when there is no such call.
func liveCommonDir(calls []envCall, dir, match string) (v string, ok bool) {
	for _, c := range calls {
		if sameDir(c.cwd, dir) && stubMatches(c.argv, match) {
			v, _ = lookupExact(c.env, "GIT_COMMON_DIR")
			return v, true
		}
	}
	return "", false
}

// namesCopy reports whether a GIT_COMMON_DIR value names a copy of the daemon,
// not the folder .probe of the call that asks git about a repository.
func namesCopy(v string) bool {
	return strings.Contains(v, shadowRootName) && filepath.Base(v) != ".probe"
}

// startTestRan reports whether a call of the start test ran.
func startTestRan(calls []envCall) bool {
	for _, c := range calls {
		if stubMatches(c.argv, "init,-q,--template=") {
			return true
		}
	}
	return false
}

// noRepoCallsIn counts the `rev-parse --git-dir` calls in the folder with no
// repository: each git.info there makes one (noRepoInfoCalls).
func noRepoCallsIn(t *testing.T, h liveFixture) int {
	t.Helper()
	n := 0
	for _, c := range readEnvCalls(t, h.log) {
		if sameDir(c.cwd, h.none) && stubMatches(c.argv, "rev-parse,--git-dir") {
			n++
		}
	}
	return n
}

// waitCalls waits up to 60 s until the log has n such calls.
func (h liveFixture) waitCalls(t *testing.T, n int) {
	t.Helper()
	for end := time.Now().Add(time.Minute); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if noRepoCallsIn(t, h) >= n {
			return
		}
	}
	t.Fatalf("fewer than %d git calls in the folder with no repository within 60 s", n)
}

// A git.info on a folder with no repository that comes while the start test
// runs waits for that test. It then answers with the state that the test gave:
// "shadow" after a good test, no member after a failed init (cells T2 and T3 of
// round task103-01, Linux VM). With no running test it answers at once, with no
// member before any test (cell T1). Mutation: in replyControl, drop the lock and
// unlock of testMu. The reply then comes before the test ends, with no member.
func TestShadowNoRepoInfoWaitsForARunningStartTest(t *testing.T) {
	for _, row := range []struct {
		name string
		fail bool
		want string
	}{
		{"good test", false, `{"isRepo":false,"repoSlug":"","defaultBranch":"","settingsControl":"shadow"}`},
		{"failed init", true, `{"isRepo":false,"repoSlug":"","defaultBranch":""}`},
	} {
		t.Run(row.name, func(t *testing.T) {
			h := newLiveFixture(t)
			h.send(t, 1, "git.info", map[string]any{"path": h.none})
			if got := liveResult(t, h.reply(t, 1)); got != `{"isRepo":false,"repoSlug":"","defaultBranch":""}` {
				t.Errorf("T1: before any test = %s, want no member", got)
			}
			holdSecond(t, "init,-q,--template=", "", row.fail)
			h.send(t, 2, "git.info", map[string]any{"path": h.repo})
			h.waitIn(t, h.gate2)
			n := noRepoCallsIn(t, h)
			h.send(t, 3, "git.info", map[string]any{"path": h.none})
			// id 3 waits only after its own git calls. Once they are in the log,
			// a request that answers at once marks a time at which id 3 had the
			// chance to answer.
			h.waitCalls(t, n+1)
			h.send(t, 4, "git.list_branches", map[string]any{"path": h.none})
			h.reply(t, 4)
			if r := h.raw(3); r != "" {
				t.Fatalf("T2: git.info answered %s while the start test ran, want a wait", r)
			}
			h.open(h.gate2)
			if got := liveResult(t, h.reply(t, 3)); got != row.want {
				t.Errorf("T2/T3: after the test = %s\n  want %s", got, row.want)
			}
		})
	}
}

// git.status and git.list_branches on a folder with no repository answer while
// the start test runs, with their normal frames (cells T5 and T5b of round
// task103-01, Linux VM). Mutation: in gitStatus, call
// req.shadow.replyControl(false) before the reply on a baseRepo with no
// repository. That status then waits, and no reply comes before the gate opens.
func TestShadowNoRepoStatusAndBranchesDoNotWait(t *testing.T) {
	h := newLiveFixture(t)
	holdSecond(t, "init,-q,--template=", "", false)
	h.send(t, 1, "git.info", map[string]any{"path": h.repo})
	h.waitIn(t, h.gate2)
	h.send(t, 2, "git.status", map[string]any{"path": h.none, "baseRepo": h.none})
	h.send(t, 3, "git.list_branches", map[string]any{"path": h.none})
	if got := liveResult(t, h.reply(t, 2)); got != `{"isRepo":false,"clean":false}` {
		t.Errorf("T5: git.status = %s", got)
	}
	if got := liveResult(t, h.reply(t, 3)); got != `{"isRepo":false,"branches":[]}` {
		t.Errorf("T5b: git.list_branches = %s", got)
	}
	if r := h.raw(1); r != "" {
		t.Fatalf("git.info answered %s before the gate opened: the test did not hold", r)
	}
}

// O1: a hold that ends while a request runs. A git.info on A without the
// member starts its first command while a git.info on B with the member runs.
// That command keeps the git directory itself. After the reply on B, a later
// command of A runs the start test and uses the copy, and A answers "shadow"
// (cell O1 of round task103-02, Linux VM). Mutation: in bindGitParams and
// keepLive, read the hold once at the start of the request (pending false, and
// atCommand returns u). A then answers "off-switch" with no test.
func TestShadowOffHoldThatEndsInARequest(t *testing.T) {
	h := newLiveFixture(t)
	slowGit(t, "remote,get-url", "gate", 10*time.Minute, "", "")
	holdSecond(t, "config,-z,--list", h.repo, false)
	h.send(t, 1, "git.info", map[string]any{"path": h.repoB, "shadowDirOff": true})
	h.waitIn(t, h.gate1)
	h.send(t, 2, "git.info", map[string]any{"path": h.repo})
	h.waitIn(t, h.gate2)
	h.open(h.gate1)
	if got := settingsControlOf(t, json.RawMessage(liveResult(t, h.reply(t, 1)))); got != controlOff {
		t.Errorf("settingsControl of B = %q, want %q", got, controlOff)
	}
	h.open(h.gate2)
	if got := settingsControlOf(t, json.RawMessage(liveResult(t, h.reply(t, 2)))); got != controlShadow {
		t.Errorf("O1: settingsControl of A = %q, want %q", got, controlShadow)
	}
	calls := readEnvCalls(t, h.log)
	if !startTestRan(calls) {
		t.Error("O1: no start test ran")
	}
	if v, ok := liveCommonDir(calls, h.repo, "rev-parse,--git-dir"); !ok || strings.Contains(v, shadowRootName) {
		t.Errorf("O1: the first command of A has GIT_COMMON_DIR=%q (found %v), want the git directory", v, ok)
	}
	if v, ok := liveCommonDir(calls, h.repo, "remote,get-url"); !ok || !namesCopy(v) {
		t.Errorf("O1: remote get-url of A has GIT_COMMON_DIR=%q (found %v), want the copy", v, ok)
	}
}

// O2: a hold that starts while a request runs. A git.info on A without the
// member passes its gate before the hold. Its config check and first call keep
// the copy, its later commands use the git directory itself, and it answers "off-switch"
// while the hold is live (cell O2 of round task103-02, Linux VM). Mutation: in
// atCommand, return u when the hold is live. remote get-url then names the
// copy. A second mutation reads replyControl(bool(p.ShadowDirOff)) at the
// reply: A then answers "shadow".
func TestShadowOffHoldThatStartsInARequest(t *testing.T) {
	h := newLiveFixture(t)
	slowGit(t, "init,-q,--template=", "gate", 10*time.Minute, "", "")
	holdSecond(t, "remote,get-url", h.repoB, false)
	h.send(t, 1, "git.info", map[string]any{"path": h.repo})
	h.waitIn(t, h.gate1)
	h.send(t, 2, "git.info", map[string]any{"path": h.repoB, "shadowDirOff": true})
	h.waitIn(t, h.gate2)
	h.open(h.gate1)
	if got := settingsControlOf(t, json.RawMessage(liveResult(t, h.reply(t, 1)))); got != controlOff {
		t.Errorf("O2: settingsControl of A = %q, want %q", got, controlOff)
	}
	h.open(h.gate2)
	if got := settingsControlOf(t, json.RawMessage(liveResult(t, h.reply(t, 2)))); got != controlOff {
		t.Errorf("settingsControl of B = %q, want %q", got, controlOff)
	}
	calls := readEnvCalls(t, h.log)
	var first string
	for _, c := range calls {
		if sameDir(c.cwd, h.repo) && stubMatches(c.argv, "rev-parse,--git-dir") {
			if v, _ := lookupExact(c.env, "GIT_COMMON_DIR"); namesCopy(v) {
				first = v
			}
		}
	}
	if first == "" {
		t.Error("O2: no rev-parse --git-dir of A names the copy, want the first command to keep it")
	}
	for _, m := range []string{"remote,get-url", "branch,--show-current"} {
		if v, ok := liveCommonDir(calls, h.repo, m); !ok || strings.Contains(v, shadowRootName) {
			t.Errorf("O2: %s of A has GIT_COMMON_DIR=%q (found %v), want the git directory", m, v, ok)
		}
	}
}

// A git.info on a missing path that comes while the start test runs waits for
// that test, and then answers "shadow". 5fd08069 does so in cell T7 of round
// task103-03 (Linux VM, 5 of 5 runs). Mutation: in gitInfo, answer a missing
// path with req.shadow.control(req.offLive()) in place of notRepo(). The reply
// then comes before the test ends, with no member.
func TestShadowMissingPathInfoWaitsForARunningStartTest(t *testing.T) {
	h := newLiveFixture(t)
	holdSecond(t, "init,-q,--template=", "", false)
	h.send(t, 1, "git.info", map[string]any{"path": h.repo})
	h.waitIn(t, h.gate2)
	h.send(t, 2, "git.info", map[string]any{"path": filepath.Join(h.none, "nope")})
	// A missing path makes no git call here. A request that answers at once,
	// sent after id 2, marks a time at which id 2 had the chance to answer.
	h.send(t, 3, "git.list_branches", map[string]any{"path": h.none})
	h.reply(t, 3)
	if r := h.raw(2); r != "" {
		t.Fatalf("T7: git.info answered %s while the start test ran, want a wait", r)
	}
	h.open(h.gate2)
	if got := liveResult(t, h.reply(t, 2)); got != `{"isRepo":false,"repoSlug":"","defaultBranch":"","settingsControl":"shadow"}` {
		t.Errorf("T7: after the test = %s, want the member \"shadow\"", got)
	}
}

// A hold that starts while a git.info waits for the start test counts at the
// reply: the git.info answers "off-switch" (R4 read at the reply, claustrum's own
// for this order, not measured). Mutation: in replyControl, return
// h.control(false) after the wait. The reply then has "shadow".
func TestShadowHoldThatStartsDuringTheWaitCountsAtTheReply(t *testing.T) {
	h := newLiveFixture(t)
	slowGit(t, "remote,get-url", "gate", 10*time.Minute, "", "")
	holdSecond(t, "init,-q,--template=", "", false)
	h.send(t, 1, "git.info", map[string]any{"path": h.repo})
	h.waitIn(t, h.gate2)
	waits := replyWaits.Load()
	h.send(t, 2, "git.info", map[string]any{"path": filepath.Join(h.none, "nope")})
	// id 2 must read the hold before the hold starts, so wait until it is in the
	// wait of replyControl.
	for end := time.Now().Add(time.Minute); replyWaits.Load() == waits; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("id 2 reached no wait within 60 s")
		}
	}
	if r := h.raw(2); r != "" {
		t.Fatalf("git.info answered %s while the start test ran, want a wait", r)
	}
	h.send(t, 4, "git.info", map[string]any{"path": h.repoB, "shadowDirOff": true})
	h.waitIn(t, h.gate1)
	h.open(h.gate2)
	if got := settingsControlOf(t, json.RawMessage(liveResult(t, h.reply(t, 2)))); got != controlOff {
		t.Errorf("settingsControl after the wait = %q, want %q", got, controlOff)
	}
}

// A hold that ends right before the first command of gitDefaultBranch. That
// command runs the gate again and takes the copy. The second command of that
// helper has the use of its caller by value, and it takes the copy too
// (claustrum's own, not measured). Mutation: in atCommand, return the use of
// the gate from the pending branch and drop the return of l.regated. The
// second command then has no copy.
func TestShadowLiveSwitchKeepsTheNewGateForALaterCommand(t *testing.T) {
	held := true
	gates := 0
	l := &liveSwitch{off: func() bool { return held }, pending: true}
	l.gate = func(bool) (gitUse, bool) {
		gates++
		return gitUse{gitDir: "copy", live: l}, true
	}
	u := gitUse{live: l}
	if got := u.atCommand(); got.gitDir != "" {
		t.Fatalf("a command in the hold has the copy %q", got.gitDir)
	}
	held = false
	for i, name := range []string{"symbolic-ref", "rev-parse --verify"} {
		if got := u.atCommand(); got.gitDir != "copy" {
			t.Errorf("command %d (%s) after the hold has gitDir %q, want the copy", i+1, name, got.gitDir)
		}
	}
	if gates != 1 {
		t.Errorf("the gate ran %d times, want 1", gates)
	}
}

// liveResult is the member result of a reply line, or the whole line when it has
// none.
func liveResult(t *testing.T, line string) string {
	t.Helper()
	var r struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &r); err != nil {
		t.Fatal(err)
	}
	if r.Result == nil {
		return line
	}
	return string(r.Result)
}

// excludesReadsIn counts the reads of the user's excludes in the log.
func excludesReadsIn(t *testing.T, h liveFixture) int {
	t.Helper()
	n := 0
	for _, c := range readEnvCalls(t, h.log) {
		if slices.Equal(c.argv, []string{"config", "--includes", "--path", "core.excludesFile"}) {
			n++
		}
	}
	return n
}

// Two git requests sent together on a fresh daemon make two reads of the
// user's excludes, one each, also while the first read still runs. 5fd08069
// does so in rounds task103-01 and task103-03 (Linux VM). Mutation: in
// userExcludesFile, hold the mutex during the read. The second request then
// waits for the first read and makes none. A second mutation stores no result:
// each later git command then reads again.
func TestShadowExcludesReadOfEachRequestWithNoResult(t *testing.T) {
	h := newLiveFixture(t)
	holdSecond(t, "config,--includes,--path,core.excludesFile", "", false)
	h.send(t, 1, "git.info", map[string]any{"path": h.repo})
	h.waitIn(t, h.gate2)
	h.send(t, 2, "git.info", map[string]any{"path": h.repo})
	end := time.Now().Add(30 * time.Second)
	for excludesReadsIn(t, h) < 2 && time.Now().Before(end) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := excludesReadsIn(t, h); n != 2 {
		t.Fatalf("%d reads of the excludes while the first read ran, want 2", n)
	}
	h.open(h.gate2)
	h.reply(t, 1)
	h.reply(t, 2)
	if n := excludesReadsIn(t, h); n != 2 {
		t.Errorf("%d reads of the excludes after both replies, want 2", n)
	}
}

// Two git requests one after the other make one read of the user's excludes:
// the second uses the stored result. 5fd08069 does so with requests 200 to
// 500 ms apart (cells O of round task103-02, Linux VM). Mutation: in
// userExcludesFile, store no result. Each git command then reads again.
func TestShadowExcludesReadStoredForALaterRequest(t *testing.T) {
	h := newLiveFixture(t)
	h.send(t, 1, "git.info", map[string]any{"path": h.repo})
	h.reply(t, 1)
	h.send(t, 2, "git.info", map[string]any{"path": h.repo})
	h.reply(t, 2)
	if n := excludesReadsIn(t, h); n != 1 {
		t.Errorf("%d reads of the excludes for two requests in turn, want 1", n)
	}
}
