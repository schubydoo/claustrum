package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// O2 on Windows: a hold that starts while a git.info runs moves its root pair
// (gitInfoRoot) to the git directory. On 5fd08069 only the first config listing
// and `rev-parse --git-dir` keep the copy. Both calls of the pair name the git
// directory (cell O2 of round task103-04, Windows VM). Mutation: in gitInfo,
// take copyOf, front and the pin from req.use in place of
// req.use.atCommand(). Both calls of the pair then name the copy in
// GIT_COMMON_DIR.
func TestShadowOffHoldThatStartsInARequestMovesTheRootPair(t *testing.T) {
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
	h.reply(t, 2)
	calls := readEnvCalls(t, h.log)
	found := false
	for i, c := range calls {
		if !stubMatches(c.argv, "rev-parse,--show-toplevel") || !sameDir(workTreeOf(c.argv), h.repo) {
			continue
		}
		found = true
		if i == 0 || !stubMatches(calls[i-1].argv, "config,-z,--list") {
			t.Fatalf("O2: the root pair of A has no config listing right before %q", c.argv)
		}
		pair := []envCall{calls[i-1], c}
		for _, p := range pair {
			v, _ := lookupExact(p.env, "GIT_COMMON_DIR")
			if strings.Contains(p.cwd, shadowRootName) || namesCopy(v) {
				t.Errorf("O2: %q of the root pair of A runs in %s with GIT_COMMON_DIR=%q, want the git directory", p.argv, p.cwd, v)
			}
		}
	}
	if !found {
		t.Fatal("O2: git.info on A ran no root pair")
	}
}

// workTreeOf is the value of the --work-tree= item of argv, or "".
func workTreeOf(argv []string) string {
	for _, a := range argv {
		if v, ok := strings.CutPrefix(a, "--work-tree="); ok {
			return v
		}
	}
	return ""
}
