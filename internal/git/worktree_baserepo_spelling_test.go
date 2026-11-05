package git

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// The baseRepo spelling rules of git.worktree_create and git.worktree_remove, from
// the f6010b97 rows of issue 429. The groups ran on Linux and macOS, and a later
// check added more rows. Windows ran its own two groups of rows. None of these
// tests run in parallel: they change the working directory, the process environment
// or the evalSymlinks seam.

// spellRemoveFrame is the git.worktree_remove refusal frame with text.
func spellRemoveFrame(t *testing.T, text string) string {
	t.Helper()
	return `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + jsonString(t, text) + `}}`
}

// spellCreateFrame is the git.worktree_create refusal frame with text and code.
func spellCreateFrame(t *testing.T, text, code string) string {
	t.Helper()
	return `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":` + jsonString(t, text) +
		`,"errorCode":"` + code + `"}}`
}

const spellRemoveOK = `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`

// With a worktreeRoot, git.worktree_remove refuses a relative, absent or ".." baseRepo
// with the texts of the worktreePath check, and names baseRepo as sent. The check
// comes after the worktreeRoot and worktreePath checks and before the shape check.
// A ".." baseRepo keeps its text under a refused daemon GIT_CONFIG_COUNT (measured).
// An empty worktreePath reads as relative there. The rows are Linux and macOS rows of
// group R. Linux ran R12 to R14 in the validation round only.
//
// Every row removes a plain directory, where the measured rows had a registered
// worktree. Only R13 and R14 reach the delete. R10 sends baseRepo T, where the row
// sent <T>/missing/... R11 runs in the working directory B, where the row ran in T.
//
// On Windows the worktreeRoot refusal comes first (measured on Windows). R10 is row
// WR2 there. An earlier build put the trust-root refusal before it for <T>\missing\..
// there (measured). b3081b51 judges that baseRepo as <T> (measured on Windows without a
// worktreeRoot), so the worktreeRoot refusal comes first for it too (not measured).
func TestWorktreeRemoveRootChecksBaseRepoSpelling(t *testing.T) {
	f := newWTFixture(t, false)
	b := filepath.Dir(f.top)
	sep := string(filepath.Separator)
	if err := os.MkdirAll(filepath.Join(f.top, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	dd := filepath.Join(b, "d..d", "T")
	if err := os.MkdirAll(dd, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dd, "init", "-q", "-b", "main")
	runGit(t, dd, "commit", "-q", "--allow-empty", "-m", "c0")
	root := filepath.Join(b, "R")
	leaf := filepath.Join(root, "cp", "w5")
	// The daemon's working directory is B, so "T" names the repository.
	t.Chdir(b)
	resetUserExcludesCache(t)
	h := newTestGitDeps()

	rel := func(p string) string {
		return "refusing to remove worktree: " + p +
			` is a relative path; choose the session folder by its absolute path, without ".."`
	}
	dotdot := func(p string) string {
		return "refusing to remove worktree: " + p +
			` contains a ".." component; choose the session folder by its absolute path, without ".."`
	}
	missing := f.top + sep + "missing" + sep + ".."
	rootDotDot := b + sep + "x" + sep + ".." + sep + "R"
	for _, r := range []struct {
		row        string
		params     map[string]any
		want       string // "" = spellRemoveOK
		plantCount bool
	}{
		{"R1", map[string]any{"baseRepo": missing, "worktreePath": leaf, "worktreeRoot": root}, dotdot(missing), false},
		{"R1 P1", map[string]any{"baseRepo": missing, "worktreePath": leaf, "worktreeRoot": root}, dotdot(missing), true},
		{"R3", map[string]any{"baseRepo": b + sep + "missing" + sep + ".." + sep + "T", "worktreePath": leaf, "worktreeRoot": root},
			dotdot(b + sep + "missing" + sep + ".." + sep + "T"), false},
		{"R4", map[string]any{"baseRepo": "T", "worktreePath": leaf, "worktreeRoot": root}, rel("T"), false},
		{"R5", map[string]any{"baseRepo": missing, "worktreePath": leaf, "worktreeRoot": "R"},
			`refusing to remove worktree: R is a relative path; choose the worktree location by its absolute path, without "..", beneath the filesystem root`, false},
		{"R6b", map[string]any{"baseRepo": missing, "worktreePath": leaf, "worktreeRoot": rootDotDot},
			"refusing to remove worktree: " + rootDotDot +
				` contains a ".." component; choose the worktree location by its absolute path, without "..", beneath the filesystem root`, false},
		{"R7", map[string]any{"baseRepo": missing, "worktreePath": "cp" + sep + "w5", "worktreeRoot": root}, rel("cp" + sep + "w5"), false},
		{"R8", map[string]any{"baseRepo": missing, "worktreePath": root + sep + "cp" + sep + "x" + sep + ".." + sep + "w5", "worktreeRoot": root},
			dotdot(root + sep + "cp" + sep + "x" + sep + ".." + sep + "w5"), false},
		{"R9", map[string]any{"baseRepo": missing, "worktreePath": filepath.Join(root, "w5"), "worktreeRoot": root}, dotdot(missing), false},
		{"R10", map[string]any{"baseRepo": f.top, "worktreePath": "", "worktreeRoot": root}, rel(""), false},
		{"R11", map[string]any{"worktreePath": leaf, "worktreeRoot": root}, rel(""), false},
		{"R12", map[string]any{"baseRepo": f.top + sep + "sub" + sep + "..", "worktreePath": leaf, "worktreeRoot": root},
			dotdot(f.top + sep + "sub" + sep + ".."), false},
		{"R13", map[string]any{"baseRepo": dd, "worktreePath": leaf, "worktreeRoot": root}, "", false},
		{"R14", map[string]any{"baseRepo": f.top + sep + ".", "worktreePath": leaf, "worktreeRoot": root}, "", false},
	} {
		t.Run(r.row, func(t *testing.T) {
			clearDaemonConfigEnv(t)
			if r.plantCount {
				t.Setenv(configCountName, "x")
			}
			if err := os.MkdirAll(leaf, 0o755); err != nil {
				t.Fatal(err)
			}
			want := spellRemoveOK
			if r.want != "" {
				want = spellRemoveFrame(t, r.want)
			}
			if msg := externalWorktreeUnsupportedRefusal(r.params["worktreeRoot"].(string), "remove"); msg != "" {
				want = spellRemoveFrame(t, msg)
			}
			got := gitDispatch(t, h, rpcLine(t, "git.worktree_remove", r.params))
			if got != want {
				t.Errorf("got  %s\nwant %s", got, want)
			}
			if want != spellRemoveOK && !exists(leaf) {
				t.Errorf("%s deleted the leaf", r.row)
			}
		})
	}
}

// Without worktreeRoot, the containment refusal names baseRepo as sent. An absent or
// empty baseRepo reads "", so a space comes before the semicolon. The daemon's
// working directory is the repository. The measured remove ran on Linux and macOS,
// and also on Windows. A measured create with an absent baseRepo ran on Linux and
// macOS. No row measured create with an empty baseRepo, or create on Windows. Those
// create answers are claustrum's own.
func TestWorktreeContainmentNamesAbsentBaseRepo(t *testing.T) {
	f := newWTFixture(t, false)
	t.Chdir(f.top)
	resetUserExcludesCache(t)
	h := newTestGitDeps()
	c1 := filepath.Join(f.top, ".claude", "worktrees", "c1")
	const guidance = "; session worktrees are only created and removed under <repository>/.claude/worktrees"
	for _, r := range []struct {
		row  string
		base map[string]any
	}{
		{"absent", map[string]any{}},
		{"empty", map[string]any{"baseRepo": ""}},
	} {
		t.Run(r.row, func(t *testing.T) {
			remove := map[string]any{"worktreePath": f.leaf()}
			create := map[string]any{"worktreePath": c1, "branchName": "c1"}
			for k, v := range r.base {
				remove[k], create[k] = v, v
			}
			want := spellRemoveFrame(t, "refusing to remove worktree: "+tickPath(f.leaf())+" is not inside the repository "+guidance)
			if got := gitDispatch(t, h, rpcLine(t, "git.worktree_remove", remove)); got != want {
				t.Errorf("remove: got  %s\nwant %s", got, want)
			}
			want = spellCreateFrame(t, "refusing to create worktree: "+tickPath(c1)+" is not inside the repository "+guidance, "unsafe_path")
			if got := gitDispatch(t, h, rpcLine(t, "git.worktree_create", create)); got != want {
				t.Errorf("create: got  %s\nwant %s", got, want)
			}
			if exists(c1) {
				t.Errorf("create made %s", c1)
			}
		})
	}
}

// Without worktreeRoot, take a present worktree whose baseRepo does not exist, such as <T>/missing/...
// Under a refused daemon GIT_CONFIG_COUNT it gets the plain lock-check refusal, and nothing is deleted.
// One measurement sends <T>/missing/.. (Linux and macOS). Another sends <B>/missing/../T (macOS). macOS
// got the same text for <T>/dl/.., with dl a dangling link. With nothing planted the remove succeeds
// (measured). On Windows these baseRepos stat, and b3081b51 judges <T>\missing\.. as <T> (measured
// on Windows). So the answers are the same there (not measured with a planted count).
func TestWorktreeRemoveCountRefusalUnresolvedBase(t *testing.T) {
	f := newWTFixture(t, false)
	runGit(t, f.top, "worktree", "add", "-q", "-b", "w1", f.leaf())
	resetUserExcludesCache(t)
	h := newTestGitDeps()
	sep := string(filepath.Separator)
	params := map[string]any{"baseRepo": f.top + sep + "missing" + sep + "..", "worktreePath": f.leaf()}

	for _, r := range []struct{ row, base string }{
		{"A1 P1", f.top + sep + "missing" + sep + ".."},
		{"A3 P1", filepath.Dir(f.top) + sep + "missing" + sep + ".." + sep + "T"},
	} {
		t.Run(r.row, func(t *testing.T) {
			clearDaemonConfigEnv(t)
			t.Setenv(configCountName, "x")
			want := spellRemoveFrame(t, lockCheckRefusal(f.leaf()))
			got := gitDispatch(t, h, rpcLine(t, "git.worktree_remove", map[string]any{"baseRepo": r.base, "worktreePath": f.leaf()}))
			if got != want {
				t.Errorf("got  %s\nwant %s", got, want)
			}
			if !exists(filepath.Join(f.leaf(), "t.txt")) || !exists(filepath.Join(f.regDir, "w1")) {
				t.Errorf("the refused remove deleted the worktree or its registration")
			}
		})
	}
	t.Run("P0", func(t *testing.T) {
		clearDaemonConfigEnv(t)
		want := spellRemoveOK
		if got := gitDispatch(t, h, rpcLine(t, "git.worktree_remove", params)); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	})
}

// A baseRepo that fails the trust-root test gets the managed-worktrees refusal on both
// methods. It comes before the worktreePath checks (measured), the Windows
// worktreeRoot refusal and the count check. Nothing is deleted or created. These rows
// are measured on Windows. The walk failure is injected for baseRepo only, through the
// evalSymlinks seam and the baseRepoWalkStat seam of Windows, so the rows run on every
// OS. Each row has its own repository, so a row fails only on its own frame. The last
// row, with the seams back, is the control.
func TestBaseRepoWalkFailureGetsTrustRootRefusal(t *testing.T) {
	old, oldStat := evalSymlinks, baseRepoWalkStat
	t.Cleanup(func() { evalSymlinks, baseRepoWalkStat = old, oldStat })
	setup := func(t *testing.T, walkFails bool) (wtFixture, *Deps) {
		t.Helper()
		f := newWTFixture(t, false)
		runGit(t, f.top, "worktree", "add", "-q", "-b", "w1", f.leaf())
		resetUserExcludesCache(t)
		evalSymlinks, baseRepoWalkStat = old, oldStat
		if walkFails {
			evalSymlinks = func(p string) (string, error) {
				if p == f.top {
					return "", errors.New("walk refused")
				}
				return old(p)
			}
			baseRepoWalkStat = func(p string) (os.FileInfo, error) {
				if p == f.top {
					return nil, errors.New("walk refused")
				}
				return oldStat(p)
			}
		}
		return f, newTestGitDeps()
	}
	sep := string(filepath.Separator)
	c1 := func(f wtFixture) string { return filepath.Join(f.top, ".claude", "worktrees", "c1") }
	tr := spellRemoveFrame(t, managedWorktreesRefusal)
	trc := spellCreateFrame(t, managedWorktreesRefusal, "nested_base_repo")
	for _, r := range []struct {
		row, method string
		params      func(f wtFixture) map[string]any
		want        string
		plantCount  bool
	}{
		{"A1", "git.worktree_remove", func(f wtFixture) map[string]any { return map[string]any{"worktreePath": f.leaf()} }, tr, false},
		{"A1 P1", "git.worktree_remove", func(f wtFixture) map[string]any { return map[string]any{"worktreePath": f.leaf()} }, tr, true},
		{"O1", "git.worktree_remove", func(wtFixture) map[string]any { return map[string]any{"worktreePath": ""} }, tr, false},
		{"O2", "git.worktree_remove", func(wtFixture) map[string]any { return map[string]any{"worktreePath": "w1"} }, tr, false},
		{"O3", "git.worktree_remove", func(f wtFixture) map[string]any {
			return map[string]any{"worktreePath": filepath.Join(f.top, ".claude", "worktrees") + sep + "x" + sep + ".." + sep + "w1"}
		}, tr, false},
		{"O4", "git.worktree_remove", func(f wtFixture) map[string]any {
			return map[string]any{"worktreePath": filepath.Join(filepath.Dir(f.top), "N", "w1")}
		}, tr, false},
		{"WR1", "git.worktree_remove", func(f wtFixture) map[string]any {
			b := filepath.Dir(f.top)
			return map[string]any{"worktreePath": filepath.Join(b, "R", "cp", "w5"), "worktreeRoot": filepath.Join(b, "R")}
		}, tr, false},
		{"C1", "git.worktree_create", func(f wtFixture) map[string]any {
			return map[string]any{"worktreePath": c1(f), "branchName": "c1"}
		}, trc, false},
		{"C1 P1", "git.worktree_create", func(f wtFixture) map[string]any {
			return map[string]any{"worktreePath": c1(f), "branchName": "c1"}
		}, trc, true},
	} {
		t.Run(r.row, func(t *testing.T) {
			f, h := setup(t, true)
			clearDaemonConfigEnv(t)
			if r.plantCount {
				t.Setenv(configCountName, "x")
			}
			params := r.params(f)
			params["baseRepo"] = f.top
			if got := gitDispatch(t, h, rpcLine(t, r.method, params)); got != r.want {
				t.Errorf("got  %s\nwant %s", got, r.want)
			}
			// A planted GIT_CONFIG_COUNT stops the git that hasRef runs.
			clearDaemonConfigEnv(t)
			if !exists(filepath.Join(f.leaf(), "t.txt")) || !exists(filepath.Join(f.regDir, "w1")) {
				t.Errorf("the refused request deleted the worktree or its registration")
			}
			if exists(c1(f)) || f.hasRef(t, "c1") {
				t.Errorf("the refused request made c1")
			}
		})
	}
	t.Run("control", func(t *testing.T) {
		f, h := setup(t, false)
		clearDaemonConfigEnv(t)
		got := gitDispatch(t, h, rpcLine(t, "git.worktree_remove", map[string]any{"baseRepo": f.top, "worktreePath": f.leaf()}))
		if got != spellRemoveOK {
			t.Errorf("got  %s\nwant %s", got, spellRemoveOK)
		}
	})
}

// On Linux and macOS <T>/missing/.. does not stat, and the answers stay. They are success
// for a gone worktree (measured), the empty-path text and not_a_repo. On Windows the path
// stats, and b3081b51 judges it as <T> (measured on Windows). The remove and the empty-path
// text are the same there, and the create makes c1 in <T>. <T>/sub/.. stats and walks on
// every OS and passes (measured, here with the worktree gone). Mutation: drop the fold arm
// of spelledWalkFails (on Windows each row of missing gets the trust-root refusal).
func TestBaseRepoMissingDotDotWalk(t *testing.T) {
	f := newWTFixture(t, false)
	if err := os.MkdirAll(filepath.Join(f.top, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	resetUserExcludesCache(t)
	h := newTestGitDeps()
	sep := string(filepath.Separator)
	missing := f.top + sep + "missing" + sep + ".."
	c1 := filepath.Join(f.top, ".claude", "worktrees", "c1")
	win := runtime.GOOS == "windows"
	created := spellCreateFrame(t, "not a git repository", "not_a_repo")
	if win {
		created = `{"jsonrpc":"2.0","id":1,"result":{"success":true,"path":` + jsonString(t, c1) +
			`,"sourceBranch":"main","branch":"c1"}}`
	}
	for _, r := range []struct {
		row, method string
		params      map[string]any
		want        string
	}{
		{"A2", "git.worktree_remove", map[string]any{"baseRepo": missing, "worktreePath": f.leaf()}, spellRemoveOK},
		{"O1", "git.worktree_remove", map[string]any{"baseRepo": missing, "worktreePath": ""},
			spellRemoveFrame(t, `failed to remove worktree: "" does not name a directory`)},
		{"C1", "git.worktree_create", map[string]any{"baseRepo": missing, "worktreePath": c1, "branchName": "c1"}, created},
		{"A4", "git.worktree_remove", map[string]any{"baseRepo": f.top + sep + "sub" + sep + "..", "worktreePath": f.leaf()}, spellRemoveOK},
	} {
		if got := gitDispatch(t, h, rpcLine(t, r.method, r.params)); got != r.want {
			t.Errorf("%s: got  %s\nwant %s", r.row, got, r.want)
		}
	}
	if made := exists(c1) && f.hasRef(t, "c1"); made != win {
		t.Errorf("create made %s and its branch: %v, want %v", c1, made, win)
	}
}

// git.worktree_create of a baseRepo that does not exist answers not_a_repo, also below a
// marker or below <T>/.claude/worktrees, and as a dangling link below a marker. Nothing
// is made (b3081b51, measured on Linux and macOS, row a also on Windows). Row c skips
// where no link can be made. Each row has its own folder. The rows match the measured
// rows a, b and c.
func TestWorktreeCreateMissingBaseRepoSkipsMarker(t *testing.T) {
	requireGit(t)
	for _, r := range []struct {
		row  string
		base func(t *testing.T, w string) string
	}{
		{"a marker", func(t *testing.T, w string) string {
			writeFile(t, filepath.Join(w, managedWorktreesMarker), "", 0o644)
			return filepath.Join(w, "sl")
		}},
		{"b worktrees", func(t *testing.T, w string) string {
			tr := filepath.Join(w, "T")
			initTrustMain(t, tr)
			return filepath.Join(tr, ".claude", "worktrees", "missing")
		}},
		{"c dangling link", func(t *testing.T, w string) string {
			writeFile(t, filepath.Join(w, managedWorktreesMarker), "", 0o644)
			sl := filepath.Join(w, "sl")
			if err := os.Symlink(filepath.Join(w, "nowhere"), sl); err != nil {
				t.Skipf("cannot make a symbolic link: %v", err)
			}
			return sl
		}},
	} {
		t.Run(r.row, func(t *testing.T) {
			w := t.TempDir()
			base := r.base(t, w)
			resetUserExcludesCache(t)
			clearDaemonConfigEnv(t)
			before := missingBaseTree(t, w)
			got := gitDispatch(t, newTestGitDeps(), rpcLine(t, "git.worktree_create", map[string]any{
				"baseRepo": base, "branchName": "w1", "worktreePath": filepath.Join(base, ".claude", "worktrees", "w1"), "sourceBranch": "main"}))
			if want := spellCreateFrame(t, "not a git repository", "not_a_repo"); got != want {
				t.Errorf("got  %s\nwant %s", got, want)
			}
			if after := missingBaseTree(t, w); !slices.Equal(after, before) {
				t.Errorf("the create changed the folder:\nbefore %v\nafter  %v", before, after)
			}
		})
	}
}

// missingBaseTree lists every path below dir, links not followed, in walk order.
func missingBaseTree(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	if err := filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
		names = append(names, p)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return names
}

// A baseRepo that os.Stat does not report as missing, and whose walk fails, gets the
// managed-worktrees refusal on both methods. Nothing is deleted or created. On Linux
// and macOS os.Stat fails there with ENOTDIR, ELOOP or EACCES. That is measured on
// remove and on create. One case uses the tracked file t.txt, where the rows used
// a.txt. On Windows <T>\t.txt\.. stats, and b3081b51 judges it as <T> (measured on
// Windows). There the remove deletes the leaf, the registration and the branch, and the
// create makes c1. G2 and G4 run on Linux and macOS only. G4 skips as root, because
// root can search a directory with mode 000. Mutation: drop the fold arm of
// spelledWalkFails (on Windows G1 and G1c get the trust-root refusal).
func TestBaseRepoStatErrorGetsTrustRootRefusal(t *testing.T) {
	sep := string(filepath.Separator)
	for _, r := range []struct {
		row      string
		unixOnly bool
		base     func(t *testing.T, f wtFixture) string
	}{
		{"G1", false, func(_ *testing.T, f wtFixture) string { return f.top + sep + "t.txt" + sep + ".." }},
		{"G2", true, func(t *testing.T, f wtFixture) string {
			if err := os.Symlink("loop", filepath.Join(f.top, "loop")); err != nil {
				t.Fatal(err)
			}
			return f.top + sep + "loop" + sep + ".."
		}},
		{"G4", true, func(t *testing.T, f wtFixture) string {
			if os.Geteuid() == 0 {
				t.Skip("root can search a directory with mode 000")
			}
			na := filepath.Join(filepath.Dir(f.top), "na")
			if err := os.MkdirAll(filepath.Join(na, "sub"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(na, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(na, 0o755) })
			return na + sep + "sub" + sep + ".." + sep + ".." + sep + "T"
		}},
	} {
		for _, create := range []bool{false, true} {
			row := r.row
			if create {
				row += "c"
			}
			t.Run(row, func(t *testing.T) {
				if r.unixOnly && runtime.GOOS == "windows" {
					t.Skip("the rows of this spelling ran on Linux and macOS")
				}
				f := newWTFixture(t, false)
				runGit(t, f.top, "worktree", "add", "-q", "-b", "w1", f.leaf())
				resetUserExcludesCache(t)
				clearDaemonConfigEnv(t)
				h := newTestGitDeps()
				c1 := filepath.Join(f.top, ".claude", "worktrees", "c1")
				base := r.base(t, f)
				method, params := "git.worktree_remove", map[string]any{"baseRepo": base, "worktreePath": f.leaf(), "branchName": "w1"}
				want := spellRemoveFrame(t, managedWorktreesRefusal)
				if create {
					method, params = "git.worktree_create", map[string]any{"baseRepo": base, "worktreePath": c1, "branchName": "c1"}
					want = spellCreateFrame(t, managedWorktreesRefusal, "nested_base_repo")
				}
				asRepo := r.row == "G1" && runtime.GOOS == "windows"
				if asRepo {
					want = spellRemoveOK
					if create {
						want = `{"jsonrpc":"2.0","id":1,"result":{"success":true,"path":` + jsonString(t, c1) +
							`,"sourceBranch":"main","branch":"c1"}}`
					}
				}
				if got := gitDispatch(t, h, rpcLine(t, method, params)); got != want {
					t.Errorf("got  %s\nwant %s", got, want)
				}
				if asRepo {
					f.wantW1(t, "T", create)
					if made := exists(c1) && f.hasRef(t, "c1"); made != create {
						t.Errorf("made c1 and its branch: %v, want %v", made, create)
					}
					return
				}
				if !exists(filepath.Join(f.leaf(), "t.txt")) || !exists(filepath.Join(f.regDir, "w1")) {
					t.Errorf("the refused request deleted the worktree or its registration")
				}
				if exists(c1) || f.hasRef(t, "c1") {
					t.Errorf("the refused request made c1")
				}
			})
		}
	}
}

// With a worktreeRoot, git.worktree_create refuses a relative, absent or ".." baseRepo
// with the texts of the worktreePath check and errorCode unsafe_path. Nothing is
// created. Measured on Linux and macOS: <T>/sub/.., "T" in the working directory B,
// and absent in the working directory T. <T>/. is the control, and it creates the
// worktree. Each row has its own repository. On Windows the worktreeRoot refusal comes
// first. No Windows row measured create with these spellings, so that answer is
// claustrum's own.
func TestWorktreeCreateRootChecksBaseRepoSpelling(t *testing.T) {
	sep := string(filepath.Separator)
	rel := func(p string) string {
		return "refusing to create worktree: " + p +
			` is a relative path; choose the session folder by its absolute path, without ".."`
	}
	dotdot := func(p string) string {
		return "refusing to create worktree: " + p +
			` contains a ".." component; choose the session folder by its absolute path, without ".."`
	}
	for _, r := range []struct {
		row    string
		inRepo bool // the working directory is T, else B
		absent bool // the request has no baseRepo member
		base   func(f wtFixture) string
		text   func(p string) string // nil for the control
	}{
		{"C7", false, false, func(f wtFixture) string { return f.top + sep + "sub" + sep + ".." }, dotdot},
		{"C8", false, false, func(wtFixture) string { return "T" }, rel},
		{"C9", true, true, func(wtFixture) string { return "" }, rel},
		{"C10", false, false, func(f wtFixture) string { return f.top + sep + "." }, nil},
	} {
		t.Run(r.row, func(t *testing.T) {
			f := newWTFixture(t, false)
			b := filepath.Dir(f.top)
			requireTempOutsideCheckout(t, b)
			if err := os.MkdirAll(filepath.Join(f.top, "sub"), 0o755); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(b, "R")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			leaf := filepath.Join(root, "cp", "c7")
			if r.inRepo {
				t.Chdir(f.top)
			} else {
				t.Chdir(b)
			}
			resetUserExcludesCache(t)
			clearDaemonConfigEnv(t)
			h := newTestGitDeps()
			params := map[string]any{"worktreePath": leaf, "worktreeRoot": root, "branchName": "c7"}
			base := r.base(f)
			if !r.absent {
				params["baseRepo"] = base
			}
			refused := true
			var want string
			switch msg := externalWorktreeUnsupportedRefusal(root, "create"); {
			case msg != "":
				want = spellCreateFrame(t, msg, "unsafe_path")
			case r.text != nil:
				want = spellCreateFrame(t, r.text(base), "unsafe_path")
			default:
				refused = false
				want = `{"jsonrpc":"2.0","id":1,"result":{"success":true,"path":` + jsonString(t, leaf) +
					`,"sourceBranch":"main","branch":"c7"}}`
			}
			if got := gitDispatch(t, h, rpcLine(t, "git.worktree_create", params)); got != want {
				t.Errorf("got  %s\nwant %s", got, want)
			}
			if made := exists(filepath.Join(root, "cp")) || f.hasRef(t, "c7"); made == refused {
				t.Errorf("made the worktree = %v, want %v", made, !refused)
			}
		})
	}
}
