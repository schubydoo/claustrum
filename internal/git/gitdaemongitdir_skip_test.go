package git

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// setCopyPinsWalkGitDir sets copyPinsWalkGitDir for one test.
func setCopyPinsWalkGitDir(t *testing.T, on bool) {
	t.Helper()
	old := copyPinsWalkGitDir
	copyPinsWalkGitDir = on
	t.Cleanup(func() { copyPinsWalkGitDir = old })
}

// skipRequests are the four requests of the rows of daemonGitDirSkipped on repo:
// git.info, git.status, a create of w1 and its remove.
func skipRequests(repo string) []struct {
	method string
	params map[string]any
} {
	leaf := filepath.Join(repo, ".claude", "worktrees", "w1")
	return []struct {
		method string
		params map[string]any
	}{
		{"git.info", map[string]any{"path": repo}},
		{"git.status", map[string]any{"path": repo, "baseRepo": repo}},
		{"git.worktree_create", map[string]any{"baseRepo": repo, "branchName": "w1", "worktreePath": leaf}},
		{"git.worktree_remove", map[string]any{"baseRepo": repo, "worktreePath": leaf, "branchName": "w1"}},
	}
}

// TestDaemonGitDirSkippedMakesTheCallsOfNoGitDir: with daemonGitDirSkipped and a
// daemon GIT_DIR of the repository's own `.git`, git.info, git.status, a create
// and a remove make the calls that they make with no daemon GIT_DIR, with the
// same folder variables. A git.info on another repository comes first. So
// git.info makes the call about the repository and the read of the config, and
// the create asks git again. 87375cc7 has these calls on Windows (measured on
// Windows). The replies are equal too.
//
// Mutations: in asksGit drop `|| daemonGitDirSkipped()` (no call about the
// repository), in gitUse.pin drop `&& !daemonGitDirSkipped()` (no GIT_DIR and
// GIT_WORK_TREE), in daemonPartFor return daemon (GIT_DIR on the calls with
// --git-dir=), and in probeEnv drop withoutSkippedGitDir.
func TestDaemonGitDirSkippedMakesTheCallsOfNoGitDir(t *testing.T) {
	setCopyPinsWalkGitDir(t, true)
	run := func(t *testing.T, withGitDir bool) (lists [][]string, replies []string) {
		f := newShadowFixture(t)
		setAttrSource(t, true)
		repo, gitDir := configRepo(t)
		if withGitDir {
			t.Setenv("GIT_DIR", gitDir)
		}
		other, _ := configRepo(t)
		if raw, _ := f.info(t, other, nil); !strings.Contains(raw, `"isRepo":true`) {
			t.Fatalf("the first git.info = %s", raw)
		}
		folder := daemonFolder(t, f.run)
		for _, m := range skipRequests(repo) {
			raw, calls := f.call(t, m.method, m.params)
			for _, old := range []string{strings.Trim(jsonQuoted(t, repo), `"`), filepath.ToSlash(repo), repo} {
				raw = strings.ReplaceAll(raw, old, "<repo>")
			}
			replies = append(replies, raw)
			var list []string
			for _, c := range calls {
				line := c.cwd + " | " + strings.Join(dropConfigArgs(c.argv), " ") + " ||"
				for _, name := range []string{"GIT_DIR", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_WORK_TREE"} {
					if v, ok := lookupExact(c.env, name); ok {
						line += " " + name + "=" + v
					}
				}
				for _, old := range []string{filepath.Join(folder, shadowRepoName(gitDir)), folder, f.run, repo} {
					line = strings.ReplaceAll(line, old, "<x>")
				}
				list = append(list, line)
			}
			lists = append(lists, list)
		}
		return lists, replies
	}
	var plain, skipped [][]string
	var plainReplies, skippedReplies []string
	t.Run("no daemon GIT_DIR", func(t *testing.T) { plain, plainReplies = run(t, false) })
	t.Run("daemon GIT_DIR", func(t *testing.T) { skipped, skippedReplies = run(t, true) })
	if len(plain) != 4 || len(skipped) != 4 {
		t.Fatalf("%d and %d requests ran, want 4 and 4", len(plain), len(skipped))
	}
	if !slices.Equal(plainReplies, skippedReplies) {
		t.Errorf("the replies differ:\n%q\n%q", plainReplies, skippedReplies)
	}
	// The object ids and the numbers of the short-lived folders differ per run.
	varying := regexp.MustCompile(`[0-9a-f]{40}|[0-9]+`)
	for i := range plain {
		a, b := varying.ReplaceAllString(strings.Join(plain[i], "\n"), "#"), varying.ReplaceAllString(strings.Join(skipped[i], "\n"), "#")
		if a != b {
			t.Errorf("request %d: %d calls with no daemon GIT_DIR and %d with one\n%s\n---\n%s", i+1, len(plain[i]), len(skipped[i]), a, b)
		}
	}
	probes := 0
	for _, line := range skipped[0] {
		if strings.Contains(line, "rev-parse --git-dir ||") && !strings.Contains(line, "GIT_DIR=") {
			probes++
		}
	}
	if probes != 1 {
		t.Errorf("git.info made %d calls about the repository, want 1:\n%s", probes, strings.Join(skipped[0], "\n"))
	}
}

// dropConfigArgs is argv without its `-c <item>` pairs.
func dropConfigArgs(argv []string) []string {
	var out []string
	for i := 0; i < len(argv); i++ {
		if argv[i] == "-c" {
			i++
			continue
		}
		out = append(out, argv[i])
	}
	return out
}

// TestDaemonGitDirSkippedPin: with daemonGitDirSkipped a call in the repository
// gets GIT_DIR and GIT_WORK_TREE of the walk after the two variables of the
// copy, and a call with --git-dir= the two variables only. The daemon's own
// GIT_DIR goes from the environment of both. With no copy it stays. Mutations:
// in gitUse.pin drop `&& !daemonGitDirSkipped()`, and in daemonPartFor return
// daemon. 87375cc7 has these variables on Windows (measured on Windows).
func TestDaemonGitDirSkippedPin(t *testing.T) {
	clearFolderEnv(t)
	f := newRepoFolderFixture(t)
	u := f.useOf(t, f.repo)
	common := u.commonVars()
	full := append(slices.Clone(common), "GIT_DIR="+f.gitDir, "GIT_WORK_TREE="+f.repo)
	t.Setenv("GIT_DIR", f.gitDir)
	setCopyPinsWalkGitDir(t, true)
	if got := u.pin(f.repo); !slices.Equal(got, full) {
		t.Errorf("pin = %q\nwant %q", got, full)
	}
	daemon := []string{"A=1", "GIT_DIR=" + f.gitDir, "B=2"}
	for _, pin := range [][]string{u.pin(f.repo), u.gitDirPin(f.repo)} {
		if got, want := daemonPartFor(daemon, pin), []string{"A=1", "B=2"}; !slices.Equal(got, want) {
			t.Errorf("daemonPartFor(%q) = %q, want %q", pin, got, want)
		}
	}
	if got := daemonPartFor(daemon, []string{"GIT_COMMON_DIR=" + f.gitDir}); !slices.Equal(got, daemon) {
		t.Errorf("no copy: daemonPartFor = %q, want %q", got, daemon)
	}
	// A daemon GIT_DIR of another repository leaves the copy that of the walk,
	// as 87375cc7 does for git.info, a create and a remove in a second repository
	// (measured on Windows).
	_, otherGitDir := configRepo(t)
	t.Setenv("GIT_DIR", otherGitDir)
	if got := u.pin(f.repo); !slices.Equal(got, full) {
		t.Errorf("GIT_DIR of another repository: pin = %q\nwant %q", got, full)
	}
	t.Setenv("GIT_DIR", f.gitDir)
	setCopyPinsWalkGitDir(t, false)
	if got := u.pin(f.repo); !slices.Equal(got, common) {
		t.Errorf("off: pin = %q\nwant %q", got, common)
	}
	if got := daemonPartFor(daemon, u.gitDirPin(f.repo)); !slices.Equal(got, daemon) {
		t.Errorf("off: daemonPartFor = %q, want %q", got, daemon)
	}
}

// TestDaemonGitDirSkippedProbes: with daemonGitDirSkipped the call about the
// repository and the `--attr-source` probe get no daemon GIT_DIR. Mutations:
// drop withoutSkippedGitDir in probeEnv, and in attrProbeCmd.
func TestDaemonGitDirSkippedProbes(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", gitDir)
	has := func(env []string) bool {
		_, ok := lookupExact(env, "GIT_DIR")
		return ok
	}
	setCopyPinsWalkGitDir(t, true)
	if env := probeEnv(t.TempDir(), t.TempDir()); has(env) {
		t.Errorf("probeEnv has GIT_DIR: %q", env)
	}
	if env := attrProbeCmd("version").Env; has(env) {
		t.Errorf("the --attr-source probe has GIT_DIR: %q", env)
	}
	setCopyPinsWalkGitDir(t, false)
	if env := attrProbeCmd("version").Env; !has(env) {
		t.Errorf("off: the --attr-source probe has no GIT_DIR: %q", env)
	}
	if v, _ := lookupExact(os.Environ(), "GIT_DIR"); v != gitDir {
		t.Fatalf("GIT_DIR = %q", v)
	}
}

// TestDaemonGitDirSkippedNeedsAFolder: only an absolute daemon GIT_DIR that names a
// folder is skipped. A regular file and a relative value keep the rules of before,
// whose refusals are those of 5fd08069 (measured on Windows) (TestGitDirTrustDaemonGitDirPlainFileWindows,
// TestGitDirTrustRelativeGitDirOnLinkedWorktreeWindows). A daemon GIT_COMMON_DIR
// keeps them too. Mutations: in daemonGitDirSkipped drop the tests of the value,
// or drop `|| !filepath.IsAbs(gd)`.
func TestDaemonGitDirSkippedNeedsAFolder(t *testing.T) {
	setCopyPinsWalkGitDir(t, true)
	clearFolderEnv(t)
	dir := t.TempDir()
	// The relative value names a folder from the working folder of the test, so
	// only the test of an absolute path refuses it.
	cwd := t.TempDir()
	if err := os.Mkdir(filepath.Join(cwd, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	file := filepath.Join(dir, "gitdir-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		name, gitDir, commonDir string
		want                    bool
	}{
		{"folder", dir, "", true},
		{"regular file", file, "", false},
		{"relative", ".git", "", false},
		{"missing", filepath.Join(dir, "gone"), "", false},
		{"with GIT_COMMON_DIR", dir, dir, false},
	} {
		t.Setenv("GIT_DIR", row.gitDir)
		t.Setenv("GIT_COMMON_DIR", row.commonDir)
		if row.commonDir == "" {
			if err := os.Unsetenv("GIT_COMMON_DIR"); err != nil {
				t.Fatal(err)
			}
		}
		if got := daemonGitDirSkipped(); got != row.want {
			t.Errorf("%s: daemonGitDirSkipped = %v, want %v", row.name, got, row.want)
		}
	}
}

// TestDaemonGitDirSkippedPlainFolder: with daemonGitDirSkipped a folder in which the
// walk finds no repository gets the verdict "no repository" and its pin, as with no
// daemon GIT_DIR. 87375cc7 gives a folder in no repository that verdict on Windows
// (measured on Windows). The daemon verdict for daemonGitDirVerdict and with the
// switch off is from the code.
// Mutation: in gitDirTrustFor drop the test of the walk.
func TestDaemonGitDirSkippedPlainFolder(t *testing.T) {
	clearFolderEnv(t)
	_, gitDir := configRepo(t)
	plain := resolveTestRoot(t, t.TempDir())
	t.Setenv("GIT_DIR", gitDir)
	setCopyPinsWalkGitDir(t, true)
	if tr := gitDirTrustFor(plain); tr.verdict != gitDirNoRepo {
		t.Errorf("verdict = %+v, want no repository", tr)
	}
	if got, want := commonDirPinEnv(plain), noRepoPin(); !slices.Equal(got, want) {
		t.Errorf("pin = %q, want %q", got, want)
	}
	if tr := daemonGitDirVerdict(plain); tr.verdict != gitDirTrusted || tr.pinCommonDir != gitDir {
		t.Errorf("daemonGitDirVerdict = %+v, want the daemon GIT_DIR trusted", tr)
	}
	setCopyPinsWalkGitDir(t, false)
	if tr := gitDirTrustFor(plain); tr.verdict != gitDirTrusted {
		t.Errorf("off: verdict = %+v, want the daemon GIT_DIR trusted", tr)
	}
}

// TestDaemonGitDirSkippedPlainFolderCalls: with daemonGitDirSkipped and an absolute
// daemon GIT_DIR of a repository, the five methods on a folder in no repository make
// the calls and give the replies of a daemon with no GIT_DIR. git.info with
// "shadowDirOff":true keeps the daemon verdict: its calls carry
// GIT_COMMON_DIR=<that git directory>. 87375cc7 does so on Windows (measured on
// Windows). Mutations: in gitDirTrustFor drop the test of the walk, or in gitInfo
// drop offDaemon.
func TestDaemonGitDirSkippedPlainFolderCalls(t *testing.T) {
	setCopyPinsWalkGitDir(t, true)
	run := func(t *testing.T, withGitDir bool) (lists []string) {
		f := newShadowFixture(t)
		setAttrSource(t, true)
		_, gitDir := configRepo(t)
		if withGitDir {
			t.Setenv("GIT_DIR", gitDir)
		}
		other, _ := configRepo(t)
		if raw, _ := f.info(t, other, nil); !strings.Contains(raw, `"isRepo":true`) {
			t.Fatalf("the first git.info = %s", raw)
		}
		plain := resolveTestRoot(t, t.TempDir())
		reqs := []struct {
			method string
			params map[string]any
		}{
			{"git.info", map[string]any{"path": plain}},
			{"git.status", map[string]any{"path": plain, "baseRepo": plain}},
			{"git.list_branches", map[string]any{"path": plain}},
			{"git.worktree_create", map[string]any{"baseRepo": plain, "branchName": "w1", "worktreePath": filepath.Join(plain, "w1")}},
			{"git.worktree_remove", map[string]any{"baseRepo": plain, "worktreePath": filepath.Join(plain, "w1"), "branchName": "w1"}},
		}
		for _, m := range reqs {
			raw, calls := f.call(t, m.method, m.params)
			line := m.method + " " + raw
			for _, c := range calls {
				line += "\n  " + strings.Join(dropConfigArgs(c.argv), " ") + " ||"
				for _, name := range []string{"GIT_DIR", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_WORK_TREE"} {
					if v, ok := lookupExact(c.env, name); ok {
						line += " " + name + "=" + v
					}
				}
			}
			for _, old := range []string{strings.Trim(jsonQuoted(t, plain), `"`), filepath.ToSlash(plain), plain} {
				line = strings.ReplaceAll(line, old, "<plain>")
			}
			lists = append(lists, line)
		}
		if withGitDir {
			_, calls := f.info(t, plain, map[string]any{"shadowDirOff": true})
			if len(calls) == 0 {
				t.Fatal("git.info with shadowDirOff made no call")
			}
			for _, c := range calls {
				if v, _ := lookupExact(c.env, "GIT_COMMON_DIR"); c.listing() && v != gitDir {
					t.Errorf("shadowDirOff: the listing has GIT_COMMON_DIR=%q, want %q", v, gitDir)
				}
			}
		}
		return lists
	}
	var plain, skipped []string
	t.Run("no daemon GIT_DIR", func(t *testing.T) { plain = run(t, false) })
	t.Run("daemon GIT_DIR", func(t *testing.T) { skipped = run(t, true) })
	varying := regexp.MustCompile(`[0-9a-f]{40}|[0-9]+`)
	if len(plain) != 5 || len(skipped) != 5 {
		t.Fatalf("%d and %d requests ran, want 5 and 5", len(plain), len(skipped))
	}
	for i := range plain {
		if a, b := varying.ReplaceAllString(plain[i], "#"), varying.ReplaceAllString(skipped[i], "#"); a != b {
			t.Errorf("request %d differs:\n%s\n---\n%s", i+1, a, b)
		}
	}
}

// TestDaemonGitDirOfAnotherRepositoryNamesOneRepository: with daemonGitDirSkipped and
// a daemon GIT_DIR of repository A, git.info and a create in repository B run each
// pinned call on one repository: GIT_DIR, GIT_COMMON_DIR (the copy),
// GIT_OBJECT_DIRECTORY and GIT_WORK_TREE all name it. The alternates file that the
// request reads is that of the same repository: a CRLF file there refuses the
// request, and one in the other repository does not. The test holds for either
// repository. At 6f30aa8b git.info runs its calls on B and reads the alternates of
// A, and the create of B is refused by the hold (B against the identity of A).
func TestDaemonGitDirOfAnotherRepositoryNamesOneRepository(t *testing.T) {
	setCopyPinsWalkGitDir(t, true)
	checksOn(t)
	type run struct {
		replies []string
		named   []string // the repository of each request, "A" or "B"
	}
	exec := func(t *testing.T, badIn string) run {
		f := newShadowFixture(t)
		setAttrSource(t, true)
		repoA, gitA := configRepo(t)
		repoB, gitB := configRepo(t)
		t.Setenv("GIT_DIR", gitA)
		roots := map[string]string{"A": repoA, "B": repoB}
		other, _ := configRepo(t)
		if raw, _ := f.info(t, other, nil); !strings.Contains(raw, `"isRepo":true`) {
			t.Fatalf("the first git.info = %s", raw)
		}
		if badIn != "" {
			info := filepath.Join(roots[badIn], ".git", "objects", "info")
			if err := os.MkdirAll(info, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(info, "alternates"), []byte("x\r\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		folder := daemonFolder(t, f.run)
		copies := map[string]string{"A": filepath.Join(folder, shadowRepoName(gitA)), "B": filepath.Join(folder, shadowRepoName(gitB))}
		repoOf := func(v string) string {
			for _, k := range []string{"A", "B"} {
				if v == copies[k] || v == roots[k] || strings.HasPrefix(v, roots[k]+string(filepath.Separator)) {
					return k
				}
			}
			return ""
		}
		var r run
		for _, m := range []struct {
			method string
			params map[string]any
		}{
			{"git.info", map[string]any{"path": repoB}},
			{"git.worktree_create", map[string]any{"baseRepo": repoB, "branchName": "w1", "worktreePath": filepath.Join(repoB, ".claude", "worktrees", "w1")}},
			{"git.status", map[string]any{"path": repoB, "baseRepo": repoB, "shadowDirOff": true}},
			{"git.info", map[string]any{"path": repoB, "shadowDirOff": true}},
		} {
			raw, calls := f.call(t, m.method, m.params)
			r.replies = append(r.replies, raw)
			seen := map[string]bool{}
			for _, c := range calls {
				if _, pinned := lookupExact(c.env, "GIT_COMMON_DIR"); !pinned {
					continue
				}
				in := map[string]bool{}
				for _, name := range []string{"GIT_DIR", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_WORK_TREE"} {
					if v, ok := lookupExact(c.env, name); ok {
						if k := repoOf(v); k != "" {
							in[k] = true
						}
					}
				}
				if len(in) > 1 {
					t.Errorf("%s: the call %q names both repositories: %q", m.method, c.argv[len(c.argv)-2:], c.env)
				}
				for k := range in {
					seen[k] = true
				}
			}
			if len(seen) > 1 {
				t.Errorf("%s: the calls name both repositories", m.method)
			}
			named := ""
			for k := range seen {
				named = k
			}
			r.named = append(r.named, named)
		}
		return r
	}
	var clean run
	t.Run("clean", func(t *testing.T) { clean = exec(t, "") })
	for i, method := range []string{"git.info", "git.worktree_create"} {
		x := clean.named[i]
		if x == "" {
			t.Fatalf("%s names no repository: %q", method, clean.replies[i])
		}
		y := map[string]string{"A": "B", "B": "A"}[x]
		t.Run(method+" bad alternates in "+x, func(t *testing.T) {
			if raw := exec(t, x).replies[i]; !strings.Contains(raw, "alternates") {
				t.Errorf("%s = %s, want the alternates refusal of %s, whose calls it runs", method, raw, x)
			}
		})
		t.Run(method+" bad alternates in "+y, func(t *testing.T) {
			if raw := exec(t, y).replies[i]; strings.Contains(raw, "alternates") {
				t.Errorf("%s = %s, want no refusal: its calls run on %s", method, raw, x)
			}
		})
	}
}

// TestDaemonGitDirEntryKeepsItsPin: with daemonGitDirSkipped and a daemon GIT_DIR of
// a linked-worktree entry, a pin with no copy of the same repository names the main
// git directory. The daemon's GIT_DIR then stays in the call, and a pin of another
// repository drops it (claustrum's own, not measured). Mutation: in
// pinNamesOtherGitDir compare the pin with the daemon GIT_DIR itself.
func TestDaemonGitDirEntryKeepsItsPin(t *testing.T) {
	clearFolderEnv(t)
	f := newRepoFolderFixture(t)
	entry := filepath.Join(f.gitDir, "worktrees", "wr")
	t.Setenv("GIT_DIR", entry)
	setCopyPinsWalkGitDir(t, true)
	daemon := []string{"A=1", "GIT_DIR=" + entry}
	if got := daemonPartFor(daemon, []string{"GIT_COMMON_DIR=" + f.gitDir}); !slices.Equal(got, daemon) {
		t.Errorf("same repository: daemonPartFor = %q, want %q", got, daemon)
	}
	_, other := configRepo(t)
	if got, want := daemonPartFor(daemon, []string{"GIT_COMMON_DIR=" + other}), []string{"A=1"}; !slices.Equal(got, want) {
		t.Errorf("another repository: daemonPartFor = %q, want %q", got, want)
	}
}

// TestDaemonGitDirSkippedLinkedWorktree: with daemonGitDirSkipped and a daemon
// GIT_DIR of the repository's own `.git`, git.status and git.info on its linked
// worktree make the calls and give the replies of a daemon with no GIT_DIR. The
// walk finds the worktree entry, which has the same repository git directory as
// the daemon GIT_DIR. So gitDirTrustFor keeps the daemon verdict, and only
// copyTrust gives the copy the entry (claustrum's own, not measured).
// Mutations: in the settings gate drop copyTrust (git.info refuses with the
// disagree text), in gitUse.namesCopy drop copyTrust (the calls of git.info
// carry GIT_DIR=<repo>/.git), and in copyTrust return t.
func TestDaemonGitDirSkippedLinkedWorktree(t *testing.T) {
	setCopyPinsWalkGitDir(t, true)
	run := func(t *testing.T, withGitDir bool) (lists []string) {
		f := newShadowFixture(t)
		setAttrSource(t, true)
		if withGitDir {
			t.Setenv("GIT_DIR", f.gitDir)
		}
		other, _ := configRepo(t)
		if raw, _ := f.info(t, other, nil); !strings.Contains(raw, `"isRepo":true`) {
			t.Fatalf("the first git.info = %s", raw)
		}
		folder := daemonFolder(t, f.run)
		for _, m := range []struct {
			method string
			params map[string]any
		}{
			{"git.status", map[string]any{"path": f.wt, "baseRepo": f.repo}},
			{"git.info", map[string]any{"path": f.wt}},
		} {
			raw, calls := f.call(t, m.method, m.params)
			line := m.method + " " + raw
			for _, c := range calls {
				line += "\n  " + c.cwd + " | " + strings.Join(dropConfigArgs(c.argv), " ") + " ||"
				for _, name := range []string{"GIT_DIR", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_WORK_TREE"} {
					if v, ok := lookupExact(c.env, name); ok {
						line += " " + name + "=" + v
					}
				}
			}
			for _, old := range []string{filepath.Join(folder, shadowRepoName(f.gitDir)), folder, f.run} {
				line = strings.ReplaceAll(line, old, "<x>")
			}
			for _, old := range []string{strings.Trim(jsonQuoted(t, f.repo), `"`), filepath.ToSlash(f.repo), f.repo} {
				line = strings.ReplaceAll(line, old, "<repo>")
			}
			lists = append(lists, line)
		}
		return lists
	}
	var plain, skipped []string
	t.Run("no daemon GIT_DIR", func(t *testing.T) { plain = run(t, false) })
	t.Run("daemon GIT_DIR", func(t *testing.T) { skipped = run(t, true) })
	if len(plain) != 2 || len(skipped) != 2 {
		t.Fatalf("%d and %d requests ran, want 2 and 2", len(plain), len(skipped))
	}
	varying := regexp.MustCompile(`[0-9a-f]{40}|[0-9]+`)
	for i := range plain {
		if a, b := varying.ReplaceAllString(plain[i], "#"), varying.ReplaceAllString(skipped[i], "#"); a != b {
			t.Errorf("request %d differs:\n%s\n---\n%s", i+1, a, b)
		}
	}
}
