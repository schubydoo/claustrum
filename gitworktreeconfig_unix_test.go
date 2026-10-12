//go:build unix

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	entryStatusNoRepo = `{"jsonrpc":"2.0","id":1,"result":{"isRepo":false,"clean":false}}`
	entryStatusClean  = `{"jsonrpc":"2.0","id":1,"result":{"isRepo":true,"clean":true}}`
)

// enableWorktreeConfig gives the repository of f the worktreeConfig switch and
// the `config.worktree` file text.
func enableWorktreeConfig(t *testing.T, f shadowFixture, text string) {
	t.Helper()
	config := filepath.Join(f.gitDir, "config")
	mustWrite(t, config, mustRead(t, config)+"[extensions]\n\tworktreeConfig = true\n")
	mustWrite(t, filepath.Join(f.gitDir, worktreeConfigName), text)
}

// dropFixtureWorktree removes the linked worktree of the fixture, and the
// folder `worktrees` with it.
func dropFixtureWorktree(t *testing.T, f shadowFixture) {
	t.Helper()
	runGit(t, f.repo, "worktree", "remove", "--force", f.wt)
	_ = os.Remove(filepath.Join(f.gitDir, worktreesSubdir))
}

func daemonWorkDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}

func unsetCalls(calls []envCall) []envCall {
	var out []envCall
	for _, c := range calls {
		if slices.Contains(c.argv, "--unset-all") {
			out = append(out, c)
		}
	}
	return out
}

// TestCreateGivesTheNewWorktreeItsConfigFile pins three things per fixture. The
// first is the `config.worktree` file of the registration that a create made.
// The second is the calls that take keys out of it. The third is the reply of
// a git.status on the new worktree.
//
// The rows are the cells 12a to 12f, 9a and 9k of val-s3p2b2-3-linux and of
// val-s3p2b2-5-linux (Linux VM). Section 1.1 and section 2 item 1 of the
// report of the second round have the file sizes and the replies. On 5fd08069
// the file holds the text of the file of the repository without `core.bare`
// and `core.worktree`: 48 bytes, 25 bytes or 0 bytes.
//
// The calls are `config --file <S>/.config-<n>/config.worktree --unset-all
// <key>`. They run in the working directory of the daemon, with
// GIT_DIR=<null device> as the one added variable. They come after the
// `worktree add` call and the read of the file. A file with settings left
// makes the git.status answer `isRepo:false`. An empty file answers clean.
func TestCreateGivesTheNewWorktreeItsConfigFile(t *testing.T) {
	useUmask(t, 0o022)
	threeKeys := "[core]\n\tbare = false\n\tfilemode = false\n\tsparsecheckout = true\n"
	twoLeft := "[core]\n\tfilemode = false\n\tsparsecheckout = true\n"
	filemode := "[core]\n\tfilemode = false\n"
	for _, row := range []struct {
		cell         string
		text         func(f shadowFixture) string
		withWorktree bool
		want         string
		keys         []string
		status       string
	}{
		{"12a three keys, with a worktree", func(shadowFixture) string { return threeKeys }, true, twoLeft, []string{"core.bare"}, entryStatusNoRepo},
		{"12b three keys, no worktree", func(shadowFixture) string { return threeKeys }, false, twoLeft, []string{"core.bare"}, entryStatusNoRepo},
		{"12c filemode only, with a worktree", func(shadowFixture) string { return filemode }, true, filemode, nil, entryStatusNoRepo},
		{"12d bare and worktree", func(f shadowFixture) string { return "[core]\n\tbare = false\n\tworktree = " + f.repo + "\n" }, false,
			"", []string{"core.bare", "core.worktree"}, entryStatusClean},
		{"12e and 9k bare = false", func(shadowFixture) string { return "[core]\n\tbare = false\n" }, false, "", []string{"core.bare"}, entryStatusClean},
		{"12f filemode only, no worktree", func(shadowFixture) string { return filemode }, false, filemode, nil, entryStatusNoRepo},
		{"9a bare = true", func(shadowFixture) string { return "[core]\n\tbare = true\n" }, false, "", []string{"core.bare"}, entryStatusClean},
	} {
		t.Run(row.cell, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			if !row.withWorktree {
				dropFixtureWorktree(t, f)
			}
			enableWorktreeConfig(t, f, row.text(f))
			if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"isRepo":true`) {
				t.Fatalf("git.info = %s", raw)
			}
			dir := daemonFolder(t, f.run)
			leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
			raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
			if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true,"path":` + jsonQuoted(t, leaf) + `,"sourceBranch":"main","branch":"w1"}}`; raw != want {
				t.Fatalf("git.worktree_create = %s\nwant %s", raw, want)
			}

			// The calls: `worktree add`, the read of the file, then one call per key.
			add := slices.IndexFunc(calls, func(c envCall) bool {
				return slices.Contains(c.argv, "worktree") && slices.Contains(c.argv, "add")
			})
			if add < 0 || add+1+len(row.keys) >= len(calls) || !calls[add+1].configRead() {
				t.Fatalf("no read of the file right after the worktree add call (call %d of %d)", add, len(calls))
			}
			if got := len(unsetCalls(calls)); got != len(row.keys) {
				t.Fatalf("%d --unset-all calls, want %d", got, len(row.keys))
			}
			file := ""
			for i, key := range row.keys {
				c := calls[add+2+i]
				if len(c.argv) != 5 || !slices.Equal(c.argv[:2], []string{"config", "--file"}) || !slices.Equal(c.argv[3:], []string{"--unset-all", key}) {
					t.Fatalf("call %d after the read = %q, want config --file <file> --unset-all %s", i+1, c.argv, key)
				}
				n, ok := strings.CutPrefix(c.argv[2], filepath.Join(dir, ".config-"))
				n, ok2 := strings.CutSuffix(n, string(filepath.Separator)+"config.worktree")
				if !ok || !ok2 || n == "" || strings.Trim(n, "0123456789") != "" {
					t.Errorf("the file of the call = %s, want %s/.config-<n>/config.worktree", c.argv[2], dir)
				}
				if file == "" {
					file = c.argv[2]
				}
				if c.argv[2] != file {
					t.Errorf("the calls name two files: %s and %s", file, c.argv[2])
				}
				if c.cwd != daemonWorkDir(t) {
					t.Errorf("the working directory of the call = %s, want that of the daemon", c.cwd)
				}
				if v, ok := lookupExact(c.env, "GIT_DIR"); !ok || v != os.DevNull {
					t.Errorf("GIT_DIR of the call = %q, %v, want the null device", v, ok)
				}
				for _, name := range []string{"LC_ALL", "LANGUAGE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_WORK_TREE", "GIT_ALLOW_PROTOCOL"} {
					if v, ok := lookupExact(c.env, name); ok {
						t.Errorf("the call has %s=%s, want no such variable", name, v)
					}
				}
			}

			// The file of the new registration.
			placed := filepath.Join(f.gitDir, worktreesSubdir, "w1", worktreeConfigName)
			fi, err := os.Lstat(placed)
			if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o644 {
				t.Fatalf("the file of the new registration: %v, %v, want a plain file of mode 0644", fi, err)
			}
			if got := mustRead(t, placed); got != row.want {
				t.Errorf("the file of the new registration = %q (%d bytes)\nwant %q (%d bytes)", got, len(got), row.want, len(row.want))
			}
			if got := mustRead(t, filepath.Join(f.gitDir, worktreeConfigName)); got != row.text(f) {
				t.Errorf("the file of the repository changed: %q", got)
			}
			// The short-lived folder is gone at the reply.
			if got, want := entryNames(t, dir), []string{shadowProbeName, shadowRepoName(f.gitDir), shadowLockName}; !slices.Equal(got, want) {
				t.Errorf("after the create the folder of the daemon holds %v, want %v", got, want)
			}

			if raw, _ := f.call(t, "git.status", map[string]any{"path": leaf, "baseRepo": f.repo}); raw != row.status {
				t.Errorf("git.status on the new worktree = %s\nwant %s", raw, row.status)
			}
		})
	}
}

// TestConfigFolderGoesBeforeTheNextGitCall: the folder `.config-<n>` of a
// create is gone when the next git call of the create runs. On 5fd08069 a
// listing each 2 ms saw it for 24 to 43 ms, the time of its `--unset-all`
// calls, and a trace has its removes in the millisecond of its mkdir (section
// 0.2 item 4 of the report of val-s3p2b2-6-linux, Linux VM). On a macOS VM it
// goes before `.index-<n>` comes and before the read-tree (row U3 of
// val-s3p2b2-6-macos). The stub holds the read-tree call, and the test looks
// at the folder of the daemon in that time.
func TestConfigFolderGoesBeforeTheNextGitCall(t *testing.T) {
	f := newSeededShadowFixture(t)
	dropFixtureWorktree(t, f)
	enableWorktreeConfig(t, f, "[core]\n\tbare = false\n")
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("git.info = %s", raw)
	}
	dir := daemonFolder(t, f.run)
	log := filepath.Join(t.TempDir(), "argv.log")
	t.Setenv("CLAUSTRUM_GITSTUB_LOG", log)
	slowGit(t, "read-tree", "pre", time.Second, "", "")
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	looks, there := 0, 0
	raw := duringRequest(t, f, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf}, func() {
		if b, _ := os.ReadFile(log); !strings.Contains(string(b), "\x1fread-tree\x1f") {
			return
		}
		looks++
		if len(entriesWith(dir, shadowConfigDirPrefix)) != 0 {
			there++
		}
	})
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s", raw)
	}
	if looks == 0 {
		t.Fatal("the test never looked during the read-tree call")
	}
	if there != 0 {
		t.Errorf("the folder .config-<n> was there at %d of %d looks during the read-tree call, want none", there, looks)
	}
	if got := mustRead(t, filepath.Join(f.gitDir, worktreesSubdir, "w1", worktreeConfigName)); got != "" {
		t.Errorf("the file of the new registration = %q, want it empty", got)
	}
}

// TestCreateWithTheMemberLeavesTheConfigFileToGit: with "shadowDirOff":true the
// calls name the git directory itself, and the file of the new worktree is
// left to git. The daemon runs no `--unset-all` call and makes no `.config-<n>`
// folder. 89cb6289 runs no such call (cells 12a to 12f of s3p2b-edges-linux,
// Linux VM). 5fd08069 is not measured with the member in those cells.
func TestCreateWithTheMemberLeavesTheConfigFileToGit(t *testing.T) {
	f := newSeededShadowFixture(t)
	dropFixtureWorktree(t, f)
	enableWorktreeConfig(t, f, "[core]\n\tbare = false\n")
	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, calls := f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf, "shadowDirOff": true})
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s", raw)
	}
	if got := unsetCalls(calls); len(got) != 0 {
		t.Errorf("%d --unset-all calls with the member, want none: %q", len(got), got[0].argv)
	}
	for _, name := range daemonFolders(t, f.run) {
		if got := entriesWith(filepath.Join(f.run, shadowRootName, name), shadowConfigDirPrefix); len(got) != 0 {
			t.Errorf("the folder of the daemon holds %v, want no such folder", got)
		}
	}
}

// optionList splits argv into the values of its `-c` items and the rest. A
// leading --attr-source option is passed over.
func optionList(argv []string) (values, rest []string) {
	i := 0
	for i < len(argv) && strings.HasPrefix(argv[i], "--attr-source=") {
		i++
	}
	for i+1 < len(argv) && argv[i] == "-c" {
		values = append(values, argv[i+1])
		i += 2
	}
	return values, argv[i:]
}

// frontKinds counts the calls of one request per kind of option list.
type frontKinds struct {
	heavy, light, readTree, temp int
}

// checkFront tests the option list of each call of one request. want are the
// values of the items that the lists must start with. dir is the folder of the
// daemon.
func checkFront(t *testing.T, what, dir string, calls []envCall, want []string) frontKinds {
	t.Helper()
	var k frontKinds
	for _, c := range calls {
		values, rest := optionList(c.argv)
		line := strings.Join(c.argv, " ")
		switch {
		case len(values) == 0:
			if !c.listing() && !c.configRead() && !c.excludesRead() && !slices.Contains(c.argv, "version") &&
				!slices.Contains(c.argv, "--show-scope") && !slices.Contains(c.argv, "--unset-all") {
				t.Errorf("%s: the call %q has no option list", what, line)
			}
		case c.isProbe():
			if !slices.Equal(values, []string{"core.hooksPath=" + os.DevNull, "core.fsmonitor=false"}) {
				t.Errorf("%s: the call about the repository has the list %q, want its own two items", what, values)
			}
		case strings.HasPrefix(rest[0], "--git-dir="+filepath.Join(dir, shadowStatusDirPrefix)):
			k.temp++
			if len(values) != len(gitHardenHeavy) || values[0] != gitHardenHeavy[0] {
				t.Errorf("%s: the call in the temporary git folder %q\nhas %d items and starts with %q, want the plain list of %d", what, rest, len(values), values[0], len(gitHardenHeavy))
			}
		case slices.Contains(rest, "read-tree"):
			k.readTree++
			if len(values) < 2+len(want) || !strings.HasPrefix(values[0], "lfs.storage=") ||
				!slices.Equal(values[1:1+len(want)], want) || values[1+len(want)] != gitHardenLight[0] {
				t.Errorf("%s: the read-tree list starts %q\nwant lfs.storage, then %q, then %s", what, values[:min(len(values), 5)], want, gitHardenLight[0])
			}
		default:
			if len(values) < len(want)+1 || !slices.Equal(values[:len(want)], want) {
				t.Errorf("%s: the list of %q starts %q, want %q first", what, rest, values[:min(len(values), 4)], want)
				continue
			}
			switch n := len(values) - len(want); {
			case n == len(gitHardenHeavy) && values[len(want)] == gitHardenHeavy[0]:
				k.heavy++
			case n >= len(gitHardenLight) && values[len(want)] == gitHardenLight[0]:
				// `rev-list` of the branch step adds its own item after the list.
				k.light++
			default:
				t.Errorf("%s: the list of %q has %d items after the first %d and goes on with %q", what, rest, n, len(want), values[len(want)])
			}
		}
	}
	return k
}

// TestWorktreeConfigItemsOnTheCalls pins the `-c` items of a `config.worktree`
// file per call kind. On 5fd08069 they are the first items of the list on
// these calls:
//
//   - the calls of git.info and git.list_branches, and `rev-parse
//     --absolute-git-dir` (30 items with two settings).
//   - the `rev-parse --show-toplevel` and `hash-object` calls of a git.status.
//   - the light calls of a create (22 items with two settings).
//
// The read-tree has them after its lfs.storage item. The four calls of a
// git.status in the temporary git folder keep the plain list of 28. A listing
// has none. The calls on a linked worktree with its own file start with the
// items of that file, and they have none of the repository's. The cells are
// 12a, A7b and 1c of val-s3p2b2-3-linux (Linux VM) and N16c of
// val-s3p2b2-3-windows (Windows VM). With the member claustrum puts no item on
// a call: git reads the file itself there. 5fd08069 is not measured with the
// member in those cells.
//
// The calls of a remove with its branch step have no row. They get the items
// by the same rule.
func TestWorktreeConfigItemsOnTheCalls(t *testing.T) {
	f := newSeededShadowFixture(t)
	// The second worktree is made before the switch is on, so that git gives it
	// no `config.worktree` file of its own.
	w2 := filepath.Join(f.repo, ".claude", "worktrees", "w2")
	runGit(t, f.repo, "worktree", "add", "-q", w2, "-b", "w2")
	enableWorktreeConfig(t, f, "[core]\n\tbare = false\n\tfilemode = false\n\tsparsecheckout = true\n")
	mustWrite(t, filepath.Join(f.gitDir, worktreesSubdir, "wr", worktreeConfigName), "[core]\n\teol = lf\n")
	ofRepo := []string{"core.filemode=false", "core.sparsecheckout=true"}
	ofEntry := []string{"core.eol=lf"}

	raw, calls := f.info(t, f.repo, nil)
	if !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("git.info = %s", raw)
	}
	dir := daemonFolder(t, f.run)
	if k := checkFront(t, "git.info", dir, calls, ofRepo); k.heavy < 4 {
		t.Errorf("git.info made %d calls with the heavy list, want at least 4", k.heavy)
	}

	raw, calls = f.info(t, f.wt, nil)
	if !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("git.info on the linked worktree = %s", raw)
	}
	if k := checkFront(t, "git.info on the linked worktree", dir, calls, ofEntry); k.heavy < 4 {
		t.Errorf("git.info on the linked worktree made %d calls with the heavy list, want at least 4", k.heavy)
	}

	raw, calls = f.call(t, "git.list_branches", map[string]any{"path": f.repo})
	if !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("git.list_branches = %s", raw)
	}
	if k := checkFront(t, "git.list_branches", dir, calls, ofRepo); k.heavy < 2 {
		t.Errorf("git.list_branches made %d calls with the heavy list, want at least 2", k.heavy)
	}

	raw, calls = f.call(t, "git.status", map[string]any{"path": w2, "baseRepo": f.repo})
	if raw != entryStatusClean {
		t.Fatalf("git.status = %s", raw)
	}
	if k := checkFront(t, "git.status", dir, calls, ofRepo); k.temp != 4 || k.heavy < 6 {
		t.Errorf("git.status made %d calls in the temporary git folder and %d other calls with the heavy list, want 4 and at least 6", k.temp, k.heavy)
	}

	leaf := filepath.Join(f.repo, ".claude", "worktrees", "w1")
	raw, calls = f.call(t, "git.worktree_create", map[string]any{"baseRepo": f.repo, "branchName": "w1", "worktreePath": leaf})
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_create = %s", raw)
	}
	if k := checkFront(t, "git.worktree_create", dir, calls, ofRepo); k.readTree != 1 || k.light < 3 || k.heavy < 1 {
		t.Errorf("git.worktree_create made %d read-tree calls, %d calls with the light list and %d with the heavy list, want 1, at least 3 and at least 1", k.readTree, k.light, k.heavy)
	}

	// Not measured: the calls of a remove with its branch step.
	raw, calls = f.call(t, "git.worktree_remove", map[string]any{"baseRepo": f.repo, "worktreePath": leaf, "branchName": "w1"})
	if !strings.Contains(raw, `"success":true`) {
		t.Fatalf("git.worktree_remove = %s", raw)
	}
	if k := checkFront(t, "git.worktree_remove", dir, calls, ofRepo); k.light < 1 {
		t.Errorf("git.worktree_remove made %d calls with the light list, want at least 1", k.light)
	}

	// With the member no list has an item of the file.
	raw, calls = f.info(t, f.repo, map[string]any{"shadowDirOff": true})
	if !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("git.info with the member = %s", raw)
	}
	if k := checkFront(t, "git.info with the member", dir, calls, nil); k.heavy < 4 {
		t.Errorf("git.info with the member made %d calls with the heavy list, want at least 4", k.heavy)
	}
}

// TestTopLevelPairCarriesTheFrontItems: the `rev-parse --show-toplevel` call of
// the pair with --git-dir= starts with the items that its caller gives, and the
// listing before it has none. On 5fd08069 that call of a git.info has
// `core.filemode=true` first, 29 items (cell N16c of val-s3p2b2-3-windows,
// Windows VM). Only Windows makes the pair in a git.info, so this test calls
// the pair itself.
func TestTopLevelPairCarriesTheFrontItems(t *testing.T) {
	f := newSeededShadowFixture(t)
	if err := os.WriteFile(f.log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	front := []string{"-c", "core.filemode=true"}
	if out, err := gitDirWorkTreeToplevel(f.gitDir, f.repo, nil, front); err != nil || out != f.repo {
		t.Fatalf("the pair = %q, %v, want %s", out, err, f.repo)
	}
	calls := slices.DeleteFunc(readEnvCalls(t, f.log), func(c envCall) bool { return c.excludesRead() })
	if len(calls) != 2 || !calls[0].listing() {
		t.Fatalf("%d calls, want the listing and the rev-parse call", len(calls))
	}
	if values, _ := optionList(calls[0].argv); len(values) != 0 {
		t.Errorf("the listing has the items %q, want none", values)
	}
	values, rest := optionList(calls[1].argv)
	if len(values) != 1+len(gitHardenHeavy) || values[0] != "core.filemode=true" || values[1] != gitHardenHeavy[0] {
		t.Errorf("the list has %d items and starts %q, want core.filemode=true and then the %d items", len(values), values[:min(len(values), 2)], len(gitHardenHeavy))
	}
	if !argvEndsWith(rest, []string{"rev-parse", "--show-toplevel"}) {
		t.Errorf("the call = %q", rest)
	}
}

// TestStatusEntryConfigReadInTheStateShadow: a git.status reads the
// `config.worktree` file of its worktree with one git call. That call has the
// form of the read of a config file. Its working directory is the root folder,
// and it carries GIT_DIR=<null device>, LC_ALL=C and LANGUAGE=C. 5fd08069 runs
// it so (cell 1c of val-s3p2b2-5-linux, call 5 of the git.status on w0, Linux
// VM). With the member the call keeps the form of 89cb6289: the working
// directory of the daemon and no added variable (rows n12 and n13).
func TestStatusEntryConfigReadInTheStateShadow(t *testing.T) {
	f := newSeededShadowFixture(t)
	enableWorktreeConfig(t, f, "[core]\n\tfilemode = false\n")
	mustWrite(t, filepath.Join(f.gitDir, worktreesSubdir, "wr", worktreeConfigName), "[core]\n\teol = lf\n")
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("git.info = %s", raw)
	}
	for _, row := range []struct {
		name  string
		extra map[string]any
		cwd   string
		vars  []string
	}{
		{"no member", nil, gitVersionDir, []string{"GIT_DIR=" + os.DevNull, "LC_ALL=C", "LANGUAGE=C"}},
		{"with the member", map[string]any{"shadowDirOff": true}, daemonWorkDir(t), nil},
	} {
		params := map[string]any{"path": f.wt, "baseRepo": f.repo}
		for k, v := range row.extra {
			params[k] = v
		}
		raw, calls := f.call(t, "git.status", params)
		if raw != entryStatusNoRepo {
			t.Fatalf("%s: git.status = %s", row.name, raw)
		}
		var reads []envCall
		for _, c := range calls {
			if c.configRead() {
				reads = append(reads, c)
			}
		}
		if len(reads) != 1 {
			t.Fatalf("%s: %d reads of a config file, want 1", row.name, len(reads))
		}
		c := reads[0]
		if c.cwd != row.cwd {
			t.Errorf("%s: the working directory of the read = %s, want %s", row.name, c.cwd, row.cwd)
		}
		var got []string
		for _, kv := range c.env {
			if n := envName(kv); n == "GIT_DIR" || n == "LC_ALL" || n == "LANGUAGE" {
				got = append(got, kv)
			}
		}
		if !slices.Equal(got, row.vars) {
			t.Errorf("%s: the variables of the read = %q, want %q", row.name, got, row.vars)
		}
	}
}

// TestEmptyMainWorktreeConfigIsNotListed: the first git.info on a repository
// with the worktreeConfig switch lists the `config.worktree` file of the git
// directory two times, and a file of 0 bytes not at all. On 5fd08069 that
// request makes 21 calls with the empty file and no read of it (cell
// p2-K-none-empty-file of val-s3p2b2-6-linux, Linux VM). With a file of 34
// bytes it lists the file two times (cell p2-K-sparsecheckoutcone-true).
func TestEmptyMainWorktreeConfigIsNotListed(t *testing.T) {
	reads := func(t *testing.T, text string) int {
		f := newSeededShadowFixture(t)
		dropFixtureWorktree(t, f)
		enableWorktreeConfig(t, f, text)
		raw, calls := f.info(t, f.repo, nil)
		if !strings.Contains(raw, `"isRepo":true`) {
			t.Fatalf("git.info = %s", raw)
		}
		n := 0
		for _, c := range calls {
			if c.configRead() {
				n++
			}
		}
		return n
	}
	var empty, cone int
	t.Run("p2-K-none-empty-file", func(t *testing.T) { empty = reads(t, "") })
	t.Run("p2-K-sparsecheckoutcone-true", func(t *testing.T) { cone = reads(t, "[core]\n\tsparsecheckoutcone = true\n") })
	if empty < 1 || cone != empty+2 {
		t.Errorf("%d reads with the empty file and %d with a file of 34 bytes\nwant the read of the config alone, and two more for the file", empty, cone)
	}
}

// TestStatusReadsTheEntryConfigASecondTime: a git.status on a worktree whose
// `config.worktree` file has a setting lists that file two times. The second
// read comes after the first `hash-object` call and right before the first
// listing in the temporary git folder. The four calls in that folder then
// start with the item of the file, before the --attr-source option. On
// 5fd08069 the read is call 9 of 23, with `-c core.sparsecheckoutcone=true` on
// those four calls (cell p2-K-sparsecheckoutcone-true of val-s3p2b2-6-linux,
// Linux VM, and row U2 of val-s3p2b2-6-macos, macOS VM). A Windows VM has it
// for `sparsecheckout = true` too (cells P2-K4 and P2-X2 of
// val-s3p2b2-6-windows). With a file of 0 bytes it makes one read and the calls keep the
// plain list (cell p2-K-none-empty-file of that round). With the member the
// second read is not measured: claustrum makes none.
func TestStatusReadsTheEntryConfigASecondTime(t *testing.T) {
	const cone = "[core]\n\tsparsecheckoutcone = true\n"
	for _, row := range []struct {
		name, text string
		extra      map[string]any
		reads      int
		front      []string
	}{
		{"p2-K-sparsecheckoutcone-true", cone, nil, 2, []string{"core.sparsecheckoutcone=true"}},
		{"P2-X2 of the Windows round: sparsecheckout", "[core]\n\tsparsecheckout = true\n", nil, 2, []string{"core.sparsecheckout=true"}},
		{"p2-K-none-empty-file", "", nil, 1, nil},
		{"with the member (not measured)", cone, map[string]any{"shadowDirOff": true}, 1, nil},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newSeededShadowFixture(t)
			enableWorktreeConfig(t, f, row.text)
			mustWrite(t, filepath.Join(f.gitDir, worktreesSubdir, "wr", worktreeConfigName), row.text)
			if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"isRepo":true`) {
				t.Fatalf("git.info = %s", raw)
			}
			params := map[string]any{"path": f.wt, "baseRepo": f.repo}
			for k, v := range row.extra {
				params[k] = v
			}
			raw, calls := f.call(t, "git.status", params)
			if raw != entryStatusClean {
				t.Fatalf("git.status = %s", raw)
			}
			// The item of the file stands before the --attr-source option.
			split := func(argv []string) (front, values, rest []string) {
				for len(argv) > 1 && argv[0] == "-c" && !strings.HasPrefix(argv[1], gitHardenHeavy[0]) {
					front, argv = append(front, argv[1]), argv[2:]
				}
				values, rest = optionList(argv)
				return front, values, rest
			}
			var reads, temp []int
			for i, c := range calls {
				_, _, rest := split(c.argv)
				switch {
				case c.configRead():
					reads = append(reads, i)
				case !c.listing() && len(rest) > 0 && strings.HasPrefix(rest[0], "--git-dir=") && slices.Contains(rest, "--work-tree="+f.wt):
					temp = append(temp, i)
				}
			}
			if len(reads) != row.reads || len(temp) != 4 {
				t.Fatalf("%d reads of a config file and %d calls in the temporary git folder, want %d and 4", len(reads), len(temp), row.reads)
			}
			for _, i := range temp {
				front, values, rest := split(calls[i].argv)
				if !slices.Equal(front, row.front) || len(values) != len(gitHardenHeavy) {
					t.Errorf("the call %q starts with the items %q and has %d more\nwant %q and then the %d items", rest, front, len(values), row.front, len(gitHardenHeavy))
				}
			}
			if row.reads != 2 {
				return
			}
			second := calls[reads[1]]
			if reads[1]+2 != temp[0] || !calls[reads[1]+1].listing() || !slices.Contains(calls[reads[1]-1].argv, "hash-object") {
				t.Errorf("the second read is call %d and the first call in the temporary git folder is call %d\nwant the read after a hash-object call and right before the listing of that call", reads[1]+1, temp[0]+1)
			}
			if second.cwd != gitVersionDir {
				t.Errorf("the working directory of the second read = %s, want %s", second.cwd, gitVersionDir)
			}
			if v, ok := lookupExact(second.env, "GIT_DIR"); !ok || v != os.DevNull {
				t.Errorf("GIT_DIR of the second read = %q, %v, want the null device", v, ok)
			}
		})
	}
}

// TestWriteWorktreeConfigGuards: the file goes into the registration of the
// create only. Each other state writes nothing.
func TestWriteWorktreeConfigGuards(t *testing.T) {
	useUmask(t, 0o022)
	const text = "[core]\n\tfilemode = false\n"
	type fixture struct {
		u         gitUse
		reg, leaf string
	}
	newReg := func(t *testing.T) fixture {
		t.Helper()
		base := resolveTestRoot(t, t.TempDir())
		gitDir := filepath.Join(base, "repo", ".git")
		reg := filepath.Join(gitDir, worktreesSubdir, "w1")
		leaf := filepath.Join(base, "leaf")
		for _, d := range []string{reg, leaf} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		mustWrite(t, filepath.Join(reg, "gitdir"), filepath.Join(leaf, ".git")+"\n")
		return fixture{u: gitUse{folder: filepath.Join(base, "S"), repoDir: filepath.Join(base, "S", "R"), gitDir: gitDir}, reg: reg, leaf: leaf}
	}
	placed := func(f fixture) string { return filepath.Join(f.reg, worktreeConfigName) }

	t.Run("the registration of the create, tests not run", func(t *testing.T) {
		f := newReg(t)
		if err := writeWorktreeConfig(f.u, f.reg, f.leaf, testedRegistration{}, []byte(text)); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Lstat(placed(f))
		if err != nil || fi.Mode().Perm() != 0o644 || mustRead(t, placed(f)) != text {
			t.Errorf("the file: %v, %v, want mode 0644 and the text", fi, err)
		}
	})
	t.Run("the registration that the tests accepted", func(t *testing.T) {
		f := newReg(t)
		tested := acceptRegistration(f.reg)
		defer tested.release()
		if err := writeWorktreeConfig(f.u, f.reg, f.leaf, tested, []byte(text)); err != nil {
			t.Fatal(err)
		}
		if got := mustRead(t, placed(f)); got != text {
			t.Errorf("the file = %q", got)
		}
	})
	t.Run("a file that is there stays", func(t *testing.T) {
		f := newReg(t)
		mustWrite(t, placed(f), "[user]\n\tname = kept\n")
		if err := writeWorktreeConfig(f.u, f.reg, f.leaf, testedRegistration{}, []byte(text)); err == nil {
			t.Error("no error for a file that is there")
		}
		if got := mustRead(t, placed(f)); got != "[user]\n\tname = kept\n" {
			t.Errorf("the file = %q, want it kept", got)
		}
	})
	t.Run("a link at the file is not followed", func(t *testing.T) {
		f := newReg(t)
		target := filepath.Join(filepath.Dir(f.leaf), "target")
		if err := os.Symlink(target, placed(f)); err != nil {
			t.Fatal(err)
		}
		if err := writeWorktreeConfig(f.u, f.reg, f.leaf, testedRegistration{}, []byte(text)); err == nil {
			t.Error("no error for a link at the file")
		}
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			t.Errorf("the target of the link: %v, want nothing written", err)
		}
	})
	t.Run("a folder below a registration", func(t *testing.T) {
		f := newReg(t)
		deep := filepath.Join(f.reg, "deep")
		if err := os.Mkdir(deep, 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(deep, "gitdir"), filepath.Join(f.leaf, ".git")+"\n")
		if err := writeWorktreeConfig(f.u, deep, f.leaf, testedRegistration{}, []byte(text)); err == nil {
			t.Error("no error for a folder that is no direct entry")
		}
		if _, err := os.Lstat(filepath.Join(deep, worktreeConfigName)); !os.IsNotExist(err) {
			t.Errorf("the file: %v, want none", err)
		}
	})
	t.Run("a registration of another repository", func(t *testing.T) {
		f, other := newReg(t), newReg(t)
		if err := writeWorktreeConfig(f.u, other.reg, other.leaf, testedRegistration{}, []byte(text)); err == nil {
			t.Error("no error for a registration of another repository")
		}
		if _, err := os.Lstat(placed(other)); !os.IsNotExist(err) {
			t.Errorf("the file: %v, want none", err)
		}
	})
	t.Run("a registration that is a link", func(t *testing.T) {
		f := newReg(t)
		// The link is relative and its target is inside the git directory: a
		// root follows such a link.
		moved := filepath.Join(f.u.gitDir, "moved")
		if err := os.Rename(f.reg, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("..", "moved"), f.reg); err != nil {
			t.Fatal(err)
		}
		if err := writeWorktreeConfig(f.u, f.reg, f.leaf, testedRegistration{}, []byte(text)); err == nil {
			t.Error("no error for a registration that is a link")
		}
		if _, err := os.Lstat(filepath.Join(moved, worktreeConfigName)); !os.IsNotExist(err) {
			t.Errorf("the file: %v, want none", err)
		}
	})
	t.Run("another folder at the path of the accepted registration", func(t *testing.T) {
		f := newReg(t)
		tested := acceptRegistration(f.reg)
		defer tested.release()
		if err := os.Rename(f.reg, f.reg+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(f.reg, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := writeWorktreeConfig(f.u, f.reg, f.leaf, tested, []byte(text)); err == nil {
			t.Error("no error for a folder that the tests did not accept")
		}
		for _, d := range []string{f.reg, f.reg + ".old"} {
			if _, err := os.Lstat(filepath.Join(d, worktreeConfigName)); !os.IsNotExist(err) {
				t.Errorf("the file in %s: %v, want none", d, err)
			}
		}
	})
	t.Run("a record that names another worktree, tests not run", func(t *testing.T) {
		f := newReg(t)
		mustWrite(t, filepath.Join(f.reg, "gitdir"), filepath.Join(filepath.Dir(f.leaf), "other", ".git")+"\n")
		if err := writeWorktreeConfig(f.u, f.reg, f.leaf, testedRegistration{}, []byte(text)); err == nil {
			t.Error("no error for a record that names another worktree")
		}
		if _, err := os.Lstat(placed(f)); !os.IsNotExist(err) {
			t.Errorf("the file: %v, want none", err)
		}
	})
	t.Run("a request that names no copy", func(t *testing.T) {
		f := newReg(t)
		if err := writeWorktreeConfig(gitUse{}, f.reg, f.leaf, testedRegistration{}, []byte(text)); err == nil {
			t.Error("no error for a request that names no copy")
		}
		if _, err := os.Lstat(placed(f)); !os.IsNotExist(err) {
			t.Errorf("the file: %v, want none", err)
		}
	})
}
