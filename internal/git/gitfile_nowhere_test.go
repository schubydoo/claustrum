package git

import (
	"os"
	"path/filepath"
	"testing"
)

// methodFrame is one request of a test and the frame that it wants.
type methodFrame struct {
	method string
	params map[string]any
	frame  string
}

// gitFileNowhereFrames is the request of each of the four methods on g and its frame.
// off adds "shadowDirOff":true to each request. With an empty text each frame is the
// "no repository" frame, with the settingsControl member of the state shadow or of
// off. Else git.info, git.status and git.list_branches answer the -32603 error with
// text, and git.worktree_create answers text with worktree_add_failed.
func gitFileNowhereFrames(t *testing.T, g, text string, off bool) []methodFrame {
	t.Helper()
	leaf := filepath.Join(g, ".claude", "worktrees", "w1")
	info := `{"isRepo":false,"repoSlug":"","defaultBranch":"","settingsControl":"shadow"}`
	if off {
		info = `{"isRepo":false,"repoSlug":"","defaultBranch":"","settingsControl":"off-switch"}`
	}
	rows := []methodFrame{
		{"git.info", map[string]any{"path": g}, resultOf(info)},
		{"git.status", map[string]any{"path": g, "baseRepo": g}, resultOf(notRepoStatus)},
		{"git.list_branches", map[string]any{"path": g}, resultOf(notRepoList)},
		{"git.worktree_create", map[string]any{"baseRepo": g, "worktreePath": leaf, "branchName": "w1"}, resultOf(unusableCreate(t, g))},
	}
	if off {
		for _, r := range rows {
			r.params["shadowDirOff"] = true
		}
	}
	if text == "" {
		return rows
	}
	for i := range rows[:3] {
		rows[i].frame = errFrame(t, text)
	}
	rows[3].frame = resultFrame(t, worktreeResult{Success: false, Error: text, ErrorCode: "worktree_add_failed"})
	return rows
}

// The four methods on a `.git` file to nowhere, on a daemon in the state shadow, with a
// stub in place of git that fails each listing with one of the two texts. So no git
// 2.56 is needed. Both texts keep the "no repository" frames, also on a subfolder, as
// the first git request of a daemon and with "shadowDirOff":true (4c111e86, measured on
// Linux with git 2.56.1 and with git 2.43). Mutations: answer the gitfile text or the
// listing refusal in noRepoCalls, or refuse the gitfile text in statusNoRepoCalls.
//
// Without "shadowDirOff":true git.info runs that call itself. Its exit 128 gets the
// second run, which exits 0, so git.info answers "no repository" (measured on Linux).
func TestGitFileNowhereFourMethodsStub(t *testing.T) {
	f := newNoRepoFixture(t)
	f.d, _ = newSeededShadowDeps(t)
	g := filepath.Join(filepath.Dir(f.n), "G")
	nowhere := filepath.Join(filepath.Dir(f.n), "nowhere", "gitdir")
	writeFile(t, filepath.Join(g, ".git"), "gitdir: "+nowhere+"\n", 0o644)
	sub := filepath.Join(g, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	rg, err := filepath.EvalSymlinks(g)
	if err != nil {
		t.Fatal(err)
	}
	text := "fatal: gitfile does not point to a valid repository: " + filepath.ToSlash(filepath.Join(rg, ".git"))
	for _, tc := range []struct {
		name, stderr string
		off, first   bool
	}{
		{"git 2.56", text + "\n", false, false},
		{"older git", "fatal: not a git repository: " + filepath.ToSlash(nowhere) + "\n", false, false},
		{"git 2.56, first request", text + "\n", false, true},
		{"git 2.56, shadowDirOff", text + "\n", true, false},
		{"older git, shadowDirOff", "fatal: not a git repository: " + filepath.ToSlash(nowhere) + "\n", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kept := f.d
			defer func() { f.d = kept }()
			stubListing(t, 128, "")
			stubStderr(t, tc.stderr, false)
			if tc.first {
				// git.info runs the start test here. Its call 7 lists the test config
				// from standard input, and a real git answers that call.
				t.Setenv("CLAUSTRUM_GITSTUB_ENVEND", "GIT_COMMON_DIR="+filepath.Join("nowhere", "gitdir"))
			}
			rows := gitFileNowhereFrames(t, g, "", tc.off)
			if !tc.off {
				// git.info runs the call about the repository in place of the listing.
				// It exits 128, and its second run exits 0 (TestGoneGitFileInfoCalls).
				rows[0].frame = noRepoShadow
			}
			subRow := rows[0]
			subRow.params = map[string]any{"path": sub}
			if tc.off {
				subRow.params["shadowDirOff"] = true
			}
			rows = append(rows, subRow)
			for _, m := range rows {
				if tc.first {
					// A daemon with a run folder that has made no git request yet.
					f.d, _ = newShadowDeps(t)
				}
				if got := f.frame(t, m.method, m.params); got != m.frame {
					t.Errorf("%s %v = %s\nwant %s", m.method, m.params, got, m.frame)
				}
			}
			if exists(filepath.Join(g, ".claude")) {
				t.Errorf("a request made %s", filepath.Join(g, ".claude"))
			}
		})
	}
}
