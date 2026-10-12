package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
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

// worktreeConfigItemKeys are the keys of a `config.worktree` file whose
// settings go to the git calls as `-c` items. 5fd08069 sends these seven:
//
//   - `-c core.filemode=false` and `-c core.sparsecheckout=true` from the file
//     of the repository (cells 12a to 12c and 12f of val-s3p2b2-3-linux, cells
//     1a and 1b of that round, Linux VM), and `-c core.filemode=true` on a
//     Windows VM (cell N16c of val-s3p2b2-3-windows).
//   - `-c core.eol=lf` from the file of a linked worktree (cell 1c of
//     val-s3p2b2-3-linux, Linux VM).
//   - `-c core.autocrlf=input`, `-c core.ignorecase=true`, `-c
//     core.symlinks=false` and `-c core.sparsecheckoutcone=true` from the file
//     of the repository. Each ran as the one key of the file (the four cells
//     p2-K of val-s3p2b2-6-linux with those names, Linux VM).
//
// `core.bare` and `core.worktree` of that file give no item (cells 12a, 12d,
// 12e, 9a and 9k of val-s3p2b2-3-linux). `core.hookspath` and `user.name` give
// none either (cells p2-K-hookspath-x and p2-K-user-name-n of
// val-s3p2b2-6-linux). Every other key is not measured: it gives no item here.
var worktreeConfigItemKeys = []string{
	"core.filemode", "core.sparsecheckout", "core.eol",
	"core.autocrlf", "core.ignorecase", "core.symlinks", "core.sparsecheckoutcone",
}

// worktreeConfigItems are the `-c` items of settings, the listing of one
// `config.worktree` file, in the order of the file. The key is the name that
// git lists, in lower case. The value is the listed value as it is. The cells
// have `false`, `true`, `lf` and `input`. Three things are not measured:
// another spelling of a value, a key with no value and a key that is set two
// times. A key with no value gives no item. The order of two items is measured
// for `core.filemode` before `core.sparsecheckout` only (cell 12a). No cell has
// one of the four keys of val-s3p2b2-6-linux beside a second key: the order of
// the file holds there too (not measured).
func worktreeConfigItems(settings []repoSetting) []string {
	var items []string
	for _, s := range settings {
		if s.hasValue && slices.Contains(worktreeConfigItemKeys, s.key) {
			items = append(items, "-c", s.key+"="+s.value)
		}
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
// in both cells. On the read-tree of a create the items come after the
// lfs.storage item (cells 12a and 12c of val-s3p2b2-3-linux).
func (u gitUse) gitDirFront() []string {
	if !u.shadow() || daemonCommonDirSet() {
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
// daemon.
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
// No poll saw the folder `.config-<n>`. So three things are not measured: the
// mode of its file, whether 5fd08069 makes the folder when it runs no
// `--unset-all` call, and when the folder goes. claustrum makes the folder in
// each such create and writes the file with mode 0600. It removes the folder
// at the reply of the create, as the index folder (request.runAtReply). The
// shutdown of the daemon removes it with the folder of the daemon.
//
// The read of the file is the read of the config gate. Three kinds of file
// give no file in the new worktree: one that is not a regular file, one over
// the size bound and one that git cannot list (not measured). An `--unset-all`
// call that fails gives none either (not measured). A request that names no
// copy comes here for the listing only: the file of its new worktree is left
// to git. macOS and Windows have no cell with such a create.
func (req *request) newWorktreeConfig(repo, gitDir string) (text []byte, ok bool) {
	if req.repoRead == nil || !req.repoRead.worktreeConfig {
		return nil, false
	}
	ctx, cancel := gitCtx()
	defer cancel()
	source, settings, ok := listConfigFileText(ctx, filepath.Join(gitDir, worktreeConfigName))
	u := req.use
	if _, named := u.names(repo); !ok || !named {
		return nil, false
	}
	dir, remove, err := u.requestDir(shadowConfigDirPrefix, "")
	if err != nil {
		logWarnf("[GitHandler] the config.worktree file of the new worktree was not made: %v", err)
		return nil, false
	}
	if u.atReply != nil {
		*u.atReply = append(*u.atReply, remove)
	} else {
		defer remove()
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		logWarnf("[GitHandler] the config.worktree file of the new worktree was not made: %v", err)
		return nil, false
	}
	defer func() { _ = root.Close() }()
	if err := writeRootFile(root, worktreeConfigName, source, 0o600); err != nil {
		logWarnf("[GitHandler] the config.worktree file of the new worktree was not made: %v", err)
		return nil, false
	}
	file := filepath.Join(dir, worktreeConfigName)
	for _, key := range worktreeConfigDropKeys {
		if !slices.ContainsFunc(settings, func(s repoSetting) bool { return s.key == key }) {
			continue
		}
		r := boundedGit(shadowCallBound, "", append(os.Environ(), "GIT_DIR="+os.DevNull),
			"config", "--file", file, "--unset-all", key)
		if r.err != nil {
			logWarnf("[GitHandler] the config.worktree file of the new worktree was not made: git config --unset-all %s: %s", key, r.detail())
			return nil, false
		}
	}
	fi, err := root.Lstat(worktreeConfigName)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > repoConfigMaxBytes {
		logWarnf("[GitHandler] the config.worktree file of the new worktree was not made: its text cannot be read")
		return nil, false
	}
	text, err = root.ReadFile(worktreeConfigName)
	if err != nil {
		logWarnf("[GitHandler] the config.worktree file of the new worktree was not made: %v", err)
		return nil, false
	}
	return text, true
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
// registration, the registration that this create made, with mode 0644. On
// 5fd08069 the file there is `f 0644` in every cell. The `wtreg` records of
// cells 12a to 12f, 1a, 1b, 9a and 9k of val-s3p2b2-3-linux show it (Linux VM,
// umask 022).
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
		!sameGitDirPath(filepath.Dir(reg), filepath.Join(filepath.Clean(u.gitDir), worktreesSubdir)) {
		return errors.New("the registration is no entry of the worktrees folder of the repository")
	}
	if tested.path == "" && adminRecordChecked && readAdminRecord(reg, leaf) != recordNamesLeaf {
		return errors.New("the registration has no gitdir record that names the new worktree")
	}
	root, err := os.OpenRoot(u.gitDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
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
	return writeRootFile(entry, worktreeConfigName, text, 0o644)
}

// listConfigFileText is listConfigFile with the text of the file beside its
// settings.
func listConfigFileText(ctx context.Context, path string) (text []byte, settings []repoSetting, ok bool) {
	f, err := openRepoFile(path)
	if err != nil || f == nil {
		return nil, nil, false
	}
	defer func() { _ = f.Close() }()
	if repoFileKindDetail(f, path, repoConfigMaxBytes, configLarge) != "" {
		return nil, nil, false
	}
	text, _, ok = readRepoFile(f, path, repoConfigMaxBytes, configLarge)
	if !ok {
		return nil, nil, false
	}
	out, _, err := listRepoConfigText(ctx, text, gitVersionDir, repoConfigListEnv())
	if err != nil {
		return nil, nil, false
	}
	return text, listedSettings(out), true
}
