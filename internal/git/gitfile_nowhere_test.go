package git

import (
	"fmt"
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
// 2.56 is needed. On Linux and macOS (gitFileNowhereAsOlderGit) both texts keep the "no
// repository" frames, also on a subfolder for git.info, also for the reply to the first
// git request of a daemon and with "shadowDirOff":true (4c111e86, measured on Linux and
// macOS with git 2.56.1). On Windows the text of git 2.56 gives the error of
// the call about the repository, also on a subfolder and as the first git request, and
// with "shadowDirOff":true the listing refusal (5fd08069, measured on Linux and Windows, the
// listing refusal on Linux). The older
// text keeps "no repository" (measured on Linux). The test runs the answers of both
// systems. On Windows git spells its path with forward slashes, and the root in quotes
// keeps backslashes, as in V-S2w. Mutations: answer the gitfile text or the listing
// refusal in noRepoCalls on Linux, drop statusGitFileNowhere, refuse the gitfile text in
// statusNoRepoCalls, or key the rule on the state of the daemon. A create runs the call
// about the repository first (gitWorktreeCreateLocked), so the stub gives its first
// run the same text. Mutation there: answer "no repository" after every answer of the
// call in gitWorktreeCreateLocked.
//
// Without "shadowDirOff":true git.info runs that call itself. Its exit 128 gets the
// second run, which exits 0, so git.info answers "no repository" (measured on Linux
// with an older git).
func TestGitFileNowhereFourMethodsStub(t *testing.T) {
	for _, older := range []bool{true, false} {
		t.Run(fmt.Sprintf("as older git %v", older), func(t *testing.T) {
			kept := gitFileNowhereAsOlderGit
			gitFileNowhereAsOlderGit = older
			t.Cleanup(func() { gitFileNowhereAsOlderGit = kept })
			gitFileNowhereFourMethods(t, older)
		})
	}
}

// gitFileNowhereFourMethods is TestGitFileNowhereFourMethodsStub with the answers of
// Linux and macOS (older) or of Windows.
func gitFileNowhereFourMethods(t *testing.T, older bool) {
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
	check := fmt.Sprintf("git could not check this repository on the remote machine: at %q: %s", rg, text)
	hooks := "config-defined hooks could not be pinned off; git not run: listing the configuration in force: exit status 128: " + text
	if older {
		check, hooks = "", ""
	}
	for _, tc := range []struct {
		name, stderr, want string
		off, first         bool
	}{
		{"git 2.56", text + "\n", check, false, false},
		{"older git", "fatal: not a git repository: " + filepath.ToSlash(nowhere) + "\n", "", false, false},
		{"git 2.56, first request", text + "\n", check, false, true},
		{"git 2.56, shadowDirOff", text + "\n", hooks, true, false},
		{"older git, shadowDirOff", "fatal: not a git repository: " + filepath.ToSlash(nowhere) + "\n", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kept := f.d
			defer func() { f.d = kept }()
			stubListing(t, 128, "")
			stubStderr(t, tc.stderr, false)
			if !tc.off && tc.want != "" {
				// The create runs the call about the repository before the listing.
				// Its first run in G gets the text of git 2.56, its second run exits
				// 0, and the create goes on to the listing.
				t.Setenv("CLAUSTRUM_GITSTUB_MATCH2", "rev-parse,--git-dir")
				t.Setenv("CLAUSTRUM_GITSTUB_DIR2", g)
				t.Setenv("CLAUSTRUM_GITSTUB_EXIT2", "128")
				t.Setenv("CLAUSTRUM_GITSTUB_STDERR2", tc.stderr)
			}
			if tc.first {
				// git.info runs the start test here. Its call 7 lists the test config
				// from standard input, and a real git answers that call.
				t.Setenv("CLAUSTRUM_GITSTUB_ENVEND", "GIT_COMMON_DIR="+filepath.Join("nowhere", "gitdir"))
			}
			rows := gitFileNowhereFrames(t, g, tc.want, tc.off)
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
