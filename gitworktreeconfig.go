package main

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// The `config.worktree` file of a repository with the worktreeConfig switch, in
// the state shadow, as 5fd08069 uses it. The git calls of that state read the
// daemon's copy of the settings, and that copy has no worktreeConfig switch
// (shadowConfigText). So git reads no `config.worktree` file there, and it
// writes none into a new registration. 5fd08069 does two things in its place:
//
//   - It puts settings of the file before the option list of its git calls, as
//     `-c` items (worktreeConfigItems).
//   - After the `worktree add` of a create it gives the new registration its
//     own `config.worktree` file (newWorktreeConfig, placeWorktreeConfig).
//
// With the member, in the state fallback-git and with no run folder the calls
// name the git directory itself. Git then uses the file by its own rules, as
// before.

// worktreeConfigItems are the `-c` items of settings, the listing of one
// `config.worktree` file. Each key of copyKeyRules gives one item, except the
// format version, the extensions and core.bare. The value is written by the
// value rule of its key. The items are sorted by key. Cell C3a of tier0-14 has
// `-c core.filemode=false -c core.quotepath=false -c core.trustctime=false`
// from `quotepath = off`, `filemode = no` and `trustctime = false`. Cell W1 of
// tier0-16 has `-c core.logallrefupdates=always -c core.sharedrepository=group
// -c core.splitindex=true` (both Linux VM). A key with a subsection gives no
// item. Not measured: a repeated key, where the last accepted value wins.
func worktreeConfigItems(settings []repoSetting) []string {
	last := map[string]string{}
	for _, s := range settings {
		if s.key == "core.repositoryformatversion" || s.key == "core.bare" || strings.HasPrefix(s.key, "extensions.") {
			continue
		}
		if v, ok := shadowValue(s); ok {
			last[s.key] = v
		}
	}
	var items []string
	for _, key := range slices.Sorted(maps.Keys(last)) {
		items = append(items, "-c", key+"="+last[key])
	}
	return items
}

// front are the `-c` items that come before the option list of a call in dir
// with no --git-dir= in its argv, or nil. A call on the repository gets the
// items of the file of the repository. A call whose GIT_DIR is the entry of a
// linked worktree gets the items of the file of that entry, and none of the
// repository's. On 5fd08069 the calls of a git.info on such a worktree start
// with `-c core.eol=lf` and have 29 items. The file of the repository has
// `filemode = false` there (cell 1c of val-s3p2b2-3-linux, Linux VM).
//
// The items are the first of the list. The cells show them on the calls of
// git.info and git.list_branches and on `rev-parse --absolute-git-dir`: 29
// items, and 30 with two settings. They show them on the `rev-parse` and
// `worktree add` calls of a create: 21 and 22 items. A listing `config -z
// --list` has none, and the call that asks git about the repository has none.
//
// Not measured, and built by the same rule: the calls of the branch step, of a
// remove, of sourceBranch, of existingBranch and of the include step. None of
// them ran in a repository with such a file. A call in the new worktree of a
// create gets none: the file of its entry is not read for it (not measured).
func (u gitUse) front(dir string) []string {
	if len(u.items) == 0 && len(u.entryItems) == 0 {
		return nil
	}
	t, ok := u.names(dir)
	if !ok {
		return nil
	}
	if t.worktreeEntry != "" {
		return u.entryItems
	}
	return u.items
}

// gitDirFront are the `-c` items of a call with --git-dir=<git directory of
// the repository> in its argv. They are the items of the file of the
// repository, or nil for a request that names no copy.
//
// 5fd08069 has them on the `rev-parse --show-toplevel` call of a git.status
// and on its four `hash-object` calls. The cells are A7b of val-s3p2b2-3-linux
// (Linux VM) and N16c of val-s3p2b2-3-windows (Windows VM). The four calls of
// a git.status with --git-dir=<S>/.gitdir-<n> keep the plain list of 28 items
// in both cells. Those four calls get the items of the file of their own
// worktree (statusRun.entryItems). On the read-tree of a create the items come after the
// lfs.storage item (cells 12a and 12c of val-s3p2b2-3-linux).
func (u gitUse) gitDirFront() []string {
	if !u.shadow() || daemonCommonDirStays() {
		return nil
	}
	return u.items
}

// withFront is argv with the items front before it, in a new slice.
func withFront(front, argv []string) []string {
	if len(front) == 0 {
		return argv
	}
	return append(slices.Clone(front), argv...)
}

// shadowConfigDirPrefix starts the name of a short-lived folder of the daemon:
// `<S>/.config-<n>`, with a decimal number. It holds the `config.worktree` file
// of a new worktree while the daemon works on it (newWorktreeConfig).
const shadowConfigDirPrefix = ".config-"

// worktreeConfigName is the name of the file, in a git directory, in the entry
// of a linked worktree and in the short-lived folder.
const worktreeConfigName = "config.worktree"

// worktreeConfigDropKeys are the keys that the daemon takes out of the file of
// a new worktree, in the order of its calls. 5fd08069 runs `--unset-all
// core.bare` and then `--unset-all core.worktree` (cells 12d and H6 of
// val-s3p2b2-3-linux and of val-s3p2b2-5-linux, Linux VM).
var worktreeConfigDropKeys = []string{"core.bare", "core.worktree"}

// newWorktreeConfig is the text of the `config.worktree` file that the new
// worktree of a create gets. It runs after the `worktree add` call of that
// create in repo. ok is false when the new worktree gets no file from the
// daemon. drop removes the short-lived folder of this step.
//
// The repository has the worktreeConfig switch and a `config.worktree` file.
// On 5fd08069 a create there lists that file with one git call right after its
// `worktree add`. With no switch or no file it makes no such call (cells 1a to
// 1m, 9a and 9k of s3p2b-edges2-linux, Linux VM). Then it runs one call for
// each key of worktreeConfigDropKeys that the listing has:
//
//	git config --file <S>/.config-<n>/config.worktree --unset-all <key>
//
// The working directory is that of the daemon, and GIT_DIR=<null device> is
// the one added variable. The cells are 12a, 12b, 12d, 12e, H6, 1b, 9a and 9k
// of val-s3p2b2-3-linux (Linux VM): 10 calls in 8 cells. Round
// val-s3p2b2-5-linux ran seven of them again with the same calls. With
// `filemode = false` alone 5fd08069 runs no such call (cells 12c, 12f and 1a
// of val-s3p2b2-3-linux).
//
// After the create the registration of the new worktree holds the text of the
// file without those two keys. It has 0 bytes where nothing is left, and 25 or
// 48 bytes in the other cells (section 1.1 of the report of
// val-s3p2b2-5-linux).
//
// The folder `.config-<n>` has mode 0700 and its file mode 0600, on 5fd08069
// too. That build removes the folder before its next git call: a listing each 2
// ms saw it for 24 to 43 ms in the 9 cells with an `--unset-all` call, and not
// in the 11 other cells. A trace of cell 12f has the mkdir and the two removes
// in one millisecond (section 0.2 item 4 of the report of val-s3p2b2-6-linux,
// Linux VM). On a macOS VM the folder goes after the file of the new
// registration is written and before `.index-<n>` comes, in 7 of 7 sightings
// (row U3 of val-s3p2b2-6-macos). On a Windows VM it was last seen 0 to 9 ms
// before the first sight of that file, in 19 of 22 create runs. The poll did
// not see the folder in the 3 others (section 0 item 6 of the report of
// val-s3p2b2-6-windows). A trace of one cell with no `--unset-all` call (12f)
// has the mkdir.
// drop removes it, and it does nothing with no folder. The create calls it
// after it wrote the file of the registration (placeWorktreeConfig). A create
// that is refused before that step calls it at its reply (not measured).
//
// The read of the file is the read of the config gate. Three kinds of file
// give no file in the new worktree: one that is not a regular file, one over
// the size bound and one that git cannot list (not measured). An `--unset-all`
// call that fails gives none either (not measured). A request that names no
// copy comes here for the listing only: the file of its new worktree is left
// to git.
func (req *request) newWorktreeConfig(repo, gitDir string) (text []byte, ok bool, drop func()) {
	drop = func() {}
	if req.repoRead == nil || !req.repoRead.worktreeConfig {
		return nil, false, drop
	}
	ctx, cancel := gitCtx()
	defer cancel()
	source, settings, _, ok := listConfigFileText(ctx, filepath.Join(gitDir, worktreeConfigName))
	u := req.use
	if _, named := u.names(repo); !ok || !named {
		return nil, false, drop
	}
	dir, remove, err := u.requestDir(shadowConfigDirPrefix, "")
	if err != nil {
		logWarnf("[GitHandler] the config.worktree file of the new worktree was not made: %v", err)
		return nil, false, drop
	}
	gone := false
	drop = func() {
		if !gone {
			gone = true
			remove()
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		logWarnf("[GitHandler] the config.worktree file of the new worktree was not made: %v", err)
		return nil, false, drop
	}
	defer func() { _ = root.Close() }()
	if err := writeRootFile(root, worktreeConfigName, source, 0o600); err != nil {
		logWarnf("[GitHandler] the config.worktree file of the new worktree was not made: %v", err)
		return nil, false, drop
	}
	file := filepath.Join(dir, worktreeConfigName)
	// The calls get the cleaning of gitChildEnv, as on 5fd08069 (cell V2 of round
	// task004-03, Linux, macOS and Windows VMs).
	for _, key := range worktreeConfigDropKeys {
		if !slices.ContainsFunc(settings, func(s repoSetting) bool { return s.key == key }) {
			continue
		}
		r := boundedGit(shadowCallBound, "", append(gitChildEnv(), "GIT_DIR="+os.DevNull),
			"config", "--file", file, "--unset-all", key)
		if r.err != nil {
			logWarnf("[GitHandler] the config.worktree file of the new worktree was not made: git config --unset-all %s: %s", key, r.detail())
			return nil, false, drop
		}
	}
	fi, err := root.Lstat(worktreeConfigName)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > repoConfigMaxBytes {
		logWarnf("[GitHandler] the config.worktree file of the new worktree was not made: its text cannot be read")
		return nil, false, drop
	}
	text, err = root.ReadFile(worktreeConfigName)
	if err != nil {
		logWarnf("[GitHandler] the config.worktree file of the new worktree was not made: %v", err)
		return nil, false, drop
	}
	return text, true, drop
}

// writeRootFile makes the new file name in root with the content data and the
// mode perm. A file that is there is an error: nothing is replaced, and no link
// is followed.
func writeRootFile(root *os.Root, name string, data []byte, perm os.FileMode) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// placeWorktreeConfig writes text as the `config.worktree` file of
// registration, the registration that this create made. The file is made with
// mode 0666, and the umask of the daemon takes its bits from that. On 5fd08069
// the file is `f 0644` with umask 022. The `wtreg` records of cells 12a to 12f,
// 1a, 1b, 9a and 9k of val-s3p2b2-3-linux show it (Linux VM). With umask 002
// it is `f 0664`, and a trace has the open with 0666 (cells p2u-12a, p2u-12d
// and p2u-12f of val-s3p2b2-6-linux, Linux VM).
//
// How the file of 5fd08069 gets there is not measured. No git call names the
// registration, and no poll saw the short-lived folder. claustrum writes a new
// file. A rename from the folder of the daemon fails where that folder and the
// repository are on two file systems.
//
// The write goes through a root at the git directory of the copy, with the
// fixed name form `worktrees/<name>/config.worktree`:
//
//   - registration must be the direct entry `<git directory>/worktrees/<name>`,
//     and a real folder, not a link.
//   - With tested set, the folder that the root opened must be the folder that
//     the tests after the add accepted. With tested not set (no answer of git,
//     and Windows), the gitdir record of the registration must name leaf where
//     that record is compared (adminRecordChecked).
//   - A `config.worktree` file that is there stays: the file is made with
//     O_EXCL.
//
// A step that fails writes nothing and logs one line. The create goes on (not
// measured).
func placeWorktreeConfig(u gitUse, registration, leaf string, tested testedRegistration, text []byte) {
	if err := writeWorktreeConfig(u, registration, leaf, tested, text); err != nil {
		logWarnf("[GitHandler] the config.worktree file of the new worktree was not written: %v", err)
	}
}

func writeWorktreeConfig(u gitUse, registration, leaf string, tested testedRegistration, text []byte) error {
	reg := filepath.Clean(registration)
	name := filepath.Base(reg)
	if registration == "" || !u.shadow() || name == "." || name == ".." ||
		!sameGitDirPath(filepath.Dir(reg), filepath.Join(filepath.Clean(u.heldGitDir()), worktreesSubdir)) {
		return errors.New("the registration is no entry of the worktrees folder of the repository")
	}
	if tested.path == "" && adminRecordChecked && readAdminRecord(reg, leaf) != recordNamesLeaf {
		return errors.New("the registration has no gitdir record that names the new worktree")
	}
	// A create that holds its git directory writes through the held folder
	// (acceptedGitDir), and opens nothing by the path.
	var root *os.Root
	if u.accepted != nil {
		root = u.accepted.root
	} else {
		opened, err := os.OpenRoot(u.gitDir)
		if err != nil {
			return err
		}
		defer func() { _ = opened.Close() }()
		root = opened
	}
	rel := filepath.Join(worktreesSubdir, name)
	if fi, err := root.Lstat(rel); err != nil || !fi.IsDir() {
		return errors.New("the registration is not a folder")
	}
	entry, err := root.OpenRoot(rel)
	if err != nil {
		return err
	}
	defer func() { _ = entry.Close() }()
	if tested.path != "" {
		if fi, err := entry.Stat("."); err != nil || tested.info == nil || !os.SameFile(fi, tested.info) {
			return errors.New("the registration is not the folder that the tests after the add accepted")
		}
	}
	return writeRootFile(entry, worktreeConfigName, text, 0o666)
}

// listConfigFileText is listConfigFile with the text of the file beside its
// settings. badLine is the syntax error text of git for the file
// (badLineDetail), or "".
func listConfigFileText(ctx context.Context, path string) (text []byte, settings []repoSetting, badLine string, ok bool) {
	f, err := openRepoFile(path)
	if err != nil || f == nil {
		return nil, nil, "", false
	}
	defer func() { _ = f.Close() }()
	if repoFileKindDetail(f, path, repoConfigMaxBytes, configLarge) != "" {
		return nil, nil, "", false
	}
	text, _, ok = readRepoFile(f, path, repoConfigMaxBytes, configLarge)
	if !ok {
		return nil, nil, "", false
	}
	out, errOut, err := listRepoConfigText(ctx, text, gitVersionDir, repoConfigListEnv())
	if err != nil {
		return nil, nil, badLineDetail(errOut, path), false
	}
	return text, listedSettings(out), "", true
}
