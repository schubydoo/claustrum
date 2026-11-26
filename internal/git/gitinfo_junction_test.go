package git

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// git.info on a Windows path that goes through a junction (walkStart). The cases
// are those of 89cb6289, measured on Windows. A Windows test is in
// gitinfo_junction_windows_test.go. Each test names the mutation that turns it red.

// junctionRepos is the fixture of those cells. P is a repository on branch main with
// the origin p/p. R is a repository on branch rmain with the origin r/r and one more
// commit, and it holds the folder inner/sub. Three links exist. <P>/J and <base>/q/J
// lead to <R>/inner, and <base>/k/KP leads to P.
type junctionRepos struct {
	base, P, R, J, QJ, KP string
}

// newJunctionRepos builds the fixture. link makes one link, a junction on Windows.
func newJunctionRepos(t *testing.T, link func(t *testing.T, link, target string)) junctionRepos {
	t.Helper()
	requireGit(t)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	clearDaemonConfigEnv(t)
	for _, k := range []string{"GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE"} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	resetUserExcludesCache(t)
	base := resolveTestRoot(t, t.TempDir())
	f := junctionRepos{
		base: base,
		P:    filepath.Join(base, "p", "P"),
		R:    filepath.Join(base, "real", "R"),
		QJ:   filepath.Join(base, "q", "J"),
		KP:   filepath.Join(base, "k", "KP"),
	}
	f.J = filepath.Join(f.P, "J")
	for _, d := range []string{filepath.Dir(f.P), filepath.Dir(f.R), filepath.Dir(f.QJ), filepath.Dir(f.KP)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	initTrustMain(t, f.P)
	setOrigin(t, f.P, "p/p", "main")
	initTrustMain(t, f.R)
	runGit(t, f.R, "branch", "-m", "rmain")
	// The commit of R is not in P, as in the cells.
	runGit(t, f.R, "commit", "-q", "--allow-empty", "-m", "r")
	setOrigin(t, f.R, "r/r", "rmain")
	if err := os.MkdirAll(filepath.Join(f.R, "inner", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	link(t, f.J, filepath.Join(f.R, "inner"))
	link(t, f.QJ, filepath.Join(f.R, "inner"))
	link(t, f.KP, f.P)
	return f
}

// setOrigin gives repo the origin https://github.com/<slug> with origin/HEAD on branch.
func setOrigin(t *testing.T, repo, slug, branch string) {
	t.Helper()
	runGit(t, repo, "remote", "add", "origin", "https://github.com/"+slug)
	runGit(t, repo, "update-ref", "refs/remotes/origin/"+branch, "HEAD")
	runGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+branch)
}

// junctionsLikeWindows makes walkStart answer on any system as it does on Windows,
// with a symlink in the place of each junction. The resolve fails for a path with one
// of the links before its last component, and it keeps a link that is the last
// component. Any other path goes to filepath.EvalSymlinks. It returns the function
// that makes such a link.
//
// The stand-in covers git.info with an absolute path only. A relative path goes to
// filepath.EvalSymlinks, and so does a path that needs its doubled separators kept.
// The four other git methods test the resolve through linkWalkFails,
// which this helper does not set. Do not use this helper for a cell of those methods.
func junctionsLikeWindows(t *testing.T) func(t *testing.T, link, target string) {
	t.Helper()
	var links []string
	oldResolve, oldSpelled, oldHolds, oldLink, oldFinal := resolveWalkLinks, walkStartsAsSpelled, pathHoldsJunction, pathHoldsLink, finalLeafPath
	t.Cleanup(func() {
		resolveWalkLinks, walkStartsAsSpelled, pathHoldsJunction, pathHoldsLink, finalLeafPath = oldResolve, oldSpelled, oldHolds, oldLink, oldFinal
	})
	walkStartsAsSpelled = true
	finalLeafPath = filepath.EvalSymlinks
	pathHoldsJunction = func(p string) bool {
		return slices.ContainsFunc(links, func(l string) bool {
			return p == l || strings.HasPrefix(p, l+string(filepath.Separator))
		})
	}
	pathHoldsLink = pathHoldsJunction
	resolveWalkLinks = func(p string) (string, error) {
		if !filepath.IsAbs(p) {
			return filepath.EvalSymlinks(p)
		}
		vol := filepath.VolumeName(p)
		cur := vol + string(filepath.Separator)
		parts := strings.FieldsFunc(p[len(vol):], func(r rune) bool { return r == '/' || r == filepath.Separator })
		for i, part := range parts {
			cur = filepath.Join(cur, part)
			if part == ".." || !slices.Contains(links, cur) {
				continue
			}
			if i < len(parts)-1 {
				return "", &os.PathError{Op: "lstat", Path: cur, Err: syscall.ENOTDIR}
			}
			return cur, nil
		}
		return filepath.EvalSymlinks(p)
	}
	return func(t *testing.T, link, target string) {
		t.Helper()
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlink: %v", err)
		}
		links = append(links, link)
	}
}

// dotGitOf is the git directory of repo as the trust check pins it.
func dotGitOf(repo string) string { return filepath.Join(repo, ".git") }

// infoFrame is the git.info result of a repository, as JSON.
func infoFrame(t *testing.T, root, branch, slug, defaultBranch string) string {
	t.Helper()
	b, err := json.Marshal(gitInfoResult{IsRepo: true, Repo: filepath.Base(root), Branch: branch,
		Root: infoRootOf(root), RepoSlug: slug, DefaultBranch: defaultBranch})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The walk of the trust check starts at the real path of a path with a junction. So
// the pin names the repository behind the junction, as 4c111e86 and 87375cc7 do
// (measured on Windows). This overrules the rows of 89cb6289, whose walk kept the
// spelled path. Mutation: in walkStart, drop the linkFreePath arm.
func TestTrustWalkStartsAtRealPath(t *testing.T) {
	keepDaemonVars(t) // the rule of Linux and macOS, also on Windows
	if runtime.GOOS == "windows" {
		t.Skip("the cells run on real junctions in TestInfoThroughJunctionWindows")
	}
	f := newJunctionRepos(t, junctionsLikeWindows(t))
	// A real symlink, which the resolve follows on every system (measured).
	if err := os.Symlink(filepath.Join(f.R, "inner"), filepath.Join(f.P, "L")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	pinOf := func(repo string) []string { return []string{"GIT_COMMON_DIR=" + dotGitOf(repo)} }
	for _, tc := range []struct {
		cell, path string
		want       []string
	}{
		{"junction before the last component", filepath.Join(f.J, "sub"), pinOf(f.R)},
		{"junction outside any repository", filepath.Join(f.QJ, "sub"), pinOf(f.R)},
		{"junction as the last component", f.J, pinOf(f.R)},
		{"no junction, P", f.P, pinOf(f.P)},
		{"no junction, R", filepath.Join(f.R, "inner", "sub"), pinOf(f.R)},
		{"folder symlink", filepath.Join(f.P, "L", "sub"), pinOf(f.R)},
		{"two junctions", filepath.Join(f.KP, "J", "sub"), pinOf(f.R)},
		{"a junction to the repository", f.KP, pinOf(f.P)},
	} {
		t.Run(tc.cell, func(t *testing.T) {
			if got := commonDirPinEnv(tc.path); !slices.Equal(got, tc.want) {
				t.Errorf("pin of %s = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
	// A daemon GIT_DIR of R is the directory that the check judges and pins (measured).
	t.Run("daemon GIT_DIR", func(t *testing.T) {
		t.Setenv("GIT_DIR", dotGitOf(f.R))
		if got, want := commonDirPinEnv(filepath.Join(f.J, "sub")), pinOf(f.R); !slices.Equal(got, want) {
			t.Errorf("pin = %q, want %q", got, want)
		}
	})
	// A daemon GIT_COMMON_DIR leaves the calls with no entry of the check (measured).
	t.Run("daemon GIT_COMMON_DIR", func(t *testing.T) {
		t.Setenv("GIT_COMMON_DIR", dotGitOf(f.R))
		if got := commonDirPinEnv(filepath.Join(f.J, "sub")); got != nil {
			t.Errorf("pin = %q, want none", got)
		}
		if tr := requestGitDirTrust(filepath.Join(f.J, "sub"), true); tr.verdict != gitDirTrusted || tr.pinCommonDir != "" {
			t.Errorf("trust = %+v, want the zero value", tr)
		}
	})
}

// A path that does not resolve and is no directory starts no walk. So the check
// leaves it to git, and git.info has no walk root. Mutation: in walkStart, drop the
// os.Stat test (the walk root turns it red).
func TestTrustWalkLeavesMissingPathToGit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the simulated links stand for junctions off Windows")
	}
	f := newJunctionRepos(t, junctionsLikeWindows(t))
	writeFile(t, filepath.Join(f.R, "inner", "file"), "x", 0o644)
	for _, p := range []string{filepath.Join(f.J, "missing"), filepath.Join(f.J, "file")} {
		if tr := gitDirTrustFor(p); tr.verdict != gitDirTrusted || tr.pinCommonDir != "" || tr.noRepoEnv != nil {
			t.Errorf("trust of %s = %+v, want the zero value", p, tr)
		}
		if root, _ := gitWalkRoot(p); root != "" {
			t.Errorf("walk root of %s = %q, want none", p, root)
		}
	}
}

// Off Windows a path that does not resolve starts no walk, whatever it is. Mutation:
// set walkStartsAsSpelled to true in gitdirtrust_unix.go.
func TestTrustWalkNeedsResolveOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows starts the walk at the spelled path")
	}
	requireGit(t)
	repo := filepath.Join(realTempDir(t), "T")
	initTrustMain(t, repo)
	old := resolveWalkLinks
	t.Cleanup(func() { resolveWalkLinks = old })
	resolveWalkLinks = func(p string) (string, error) {
		return "", &os.PathError{Op: "lstat", Path: p, Err: syscall.ENOTDIR}
	}
	if tr := gitDirTrustFor(repo); tr.pinCommonDir != "" {
		t.Errorf("trust = %+v, want no pin", tr)
	}
	if root, _ := gitWalkRoot(repo); root != "" {
		t.Errorf("walk root = %q, want none", root)
	}
}

// checkJunctionInfoCells asserts the git.info result of each cell on the fixture f.
// A path through a junction answers as the repository behind it (4c111e86 and
// 87375cc7, measured on Windows on <P>\J\sub and <K>\J\sub). The other cells follow
// from that walk (claustrum's own, not measured).
func checkJunctionInfoCells(t *testing.T, f junctionRepos) {
	t.Helper()
	sub := filepath.Join(f.J, "sub")
	behind := infoFrame(t, f.R, "rmain", "r/r", "rmain")
	t.Run("junction before the last component", func(t *testing.T) {
		wantResult(t, "info", info(t, sub), behind)
	})
	t.Run("junction as the last component", func(t *testing.T) {
		wantResult(t, "info", info(t, f.J), behind)
	})
	t.Run("no junction, P", func(t *testing.T) {
		wantResult(t, "info", info(t, f.P), infoFrame(t, f.P, "main", "p/p", "main"))
	})
	t.Run("no junction, R", func(t *testing.T) {
		wantResult(t, "info", info(t, filepath.Join(f.R, "inner", "sub")), behind)
	})
	t.Run("junction outside any repository", func(t *testing.T) {
		wantResult(t, "info", info(t, filepath.Join(f.QJ, "sub")), behind)
	})
	t.Run("P has no origin", func(t *testing.T) {
		t.Cleanup(func() { setOrigin(t, f.P, "p/p", "main") })
		runGit(t, f.P, "remote", "remove", "origin")
		wantResult(t, "info", info(t, sub), behind)
	})
	t.Run("R has no origin", func(t *testing.T) {
		t.Cleanup(func() { setOrigin(t, f.R, "r/r", "rmain") })
		runGit(t, f.R, "remote", "remove", "origin")
		wantResult(t, "info", info(t, sub), infoFrame(t, f.R, "rmain", "", ""))
	})
}

// With the walk of Windows and a symlink in the place of each junction, git.info
// answers as the repository behind the junction. No reference build ran in this
// state off Windows. Mutation: in walkStart, drop the linkFreePath arm.
func TestInfoThroughSimulatedJunction(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the cells run on real junctions in TestInfoThroughJunctionWindows")
	}
	f := newJunctionRepos(t, junctionsLikeWindows(t))
	checkJunctionInfoCells(t, f)
}
