package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// git.status as 89cb6289 answers it. The request passes a gate first, and then four
// git commands build `changes`. docs/PROTOCOL.md → git.status gives the rules. Each
// rule names its rows. The rows are from Linux, macOS and Windows VMs, side by side
// against 89cb6289.
//
// The gate does not ask git inside `path`. It reads the worktree entries under the
// common directory of baseRepo and looks for the one entry that names `path`. So a
// damaged or missing `.git` inside `path` plays no part (rows G01 to G12, n26c and
// n26d), and no git call reads an entry file before the daemon judged it.

const (
	// statusEntryFileMaxBytes bounds the `gitdir` and `commondir` files of an entry.
	// Exactly 1 MiB passes, and one byte more fails (rows n05b, n05c and n19g). The
	// bound of `HEAD` and `config.worktree` is not measured. claustrum uses the same
	// value for them.
	statusEntryFileMaxBytes = 1 << 20
	// statusLineMaxBytes is the longest entry of the porcelain output. A longer one is
	// cut to this many bytes, and "…" follows (row o1).
	statusLineMaxBytes = 512
	// statusMaxLines is the most porcelain entries of one answer. No entry marks the
	// cut (row o2).
	statusMaxLines = 10000
	// statusMaxSubmoduleEntries is the most submodule entries of one answer. One more
	// entry then gives the count of the rest (rows o10 and n20d).
	statusMaxSubmoduleEntries = 100
	// statusNameMaxBytes is the longest submodule name. A longer one is cut to this
	// many bytes, and "…" follows inside the quotes (row o11).
	statusNameMaxBytes = 200

	gitlinkMode = "160000"

	// The three texts of a submodule entry, as the frames of 89cb6289 show them (rows
	// o4 to o13 on a Linux VM, and n20 on Linux, macOS and Windows VMs).
	submodulePresentText   = " (submodule present; contents not inspected)"
	submoduleStagedText    = " (submodule change staged; contents not inspected)"
	submoduleUnreadText    = " (submodule; could not be inspected)"
	submoduleOverflowStart = " S … (+"
	submoduleOverflowEnd   = " more submodule entries)"
)

// statusIndexMaxBytes bounds the `index` of an entry. Exactly 1 GiB is copied (row
// o15e), and one byte more answers isRepo:false with no temporary folder (row o15c).
// An `index` of 1 GiB beside a `sharedindex.x` of 1 GiB + 1 answers isRepo:false too
// (row o16). Whether 89cb6289 bounds each file or their sum is not measured. claustrum
// bounds each file. It uses the same bound for `info/sparse-checkout` and for each
// reftable table, which is not measured. (var, not const, so tests can shrink it.)
var statusIndexMaxBytes int64 = 1 << 30

// statusConfigKeys are the keys a `config.worktree` of an entry can hold. A file with
// core.sparseCheckout or index.sparse passes (rows n12 and n12b), and so does one with
// core.sparseCheckoutCone (rows o17 and v3a). A file with user.name or core.bare
// answers isRepo:false (rows n13 and v3b). No other key is measured. claustrum answers
// isRepo:false for each of them.
var statusConfigKeys = []string{"core.sparsecheckout", "core.sparsecheckoutcone", "index.sparse"}

// statusRefusal is a refusal text that goes on the wire as it is.
type statusRefusal string

func (r statusRefusal) Error() string { return string(r) }

// statusBaseInManagedTree reports whether baseRepo is refused before any git call.
// Two cases answer isRepo:false with no git call:
//
//   - baseRepo has the components `.claude/worktrees` in it, or is that folder (rows
//     n10, n10b1 and n10c1). `X/.claude` alone passes (rows n10b2 and n10c2).
//   - A folder above baseRepo, at any level, holds an entry named
//     `.claude-managed-worktrees` and no `.git` (rows o23, o23c and o23d, Linux VM).
//     The entry can be a file or a directory. With a `.git` in that folder the request
//     passes (row o23b).
//
// A marker inside baseRepo itself is ignored (row v5, Linux VM). claustrum tests
// baseRepo as sent and with its symlinks resolved. Not measured: a baseRepo that
// reaches such a folder only through a symlink.
func statusBaseInManagedTree(baseRepo string) bool {
	for _, p := range []string{baseRepo, canonicalPath(baseRepo)} {
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		for cur := abs; ; {
			parent := filepath.Dir(cur)
			if parent == cur {
				break
			}
			if filepath.Base(cur) == worktreesSubdir && filepath.Base(parent) == claudeDirName {
				return true
			}
			if _, err := os.Lstat(filepath.Join(parent, managedWorktreesMarker)); err == nil {
				if _, err := os.Lstat(filepath.Join(parent, ".git")); err != nil {
					return true
				}
			}
			cur = parent
		}
	}
	return false
}

// statusNoRepoCalls runs the two calls that 89cb6289 makes in a baseRepo where the
// trust check finds no repository: the listing, then the heavy `rev-parse
// --absolute-git-dir`. Both carry GIT_DIR=<null device> in place of the GIT_COMMON_DIR
// pin, so git finds no repository either, and the answer is isRepo:false (row n10b2 on
// Linux, macOS and Windows VMs). claustrum does not read their answers.
func statusNoRepoCalls(baseRepo string) {
	ctx, cancel := gitCtx()
	defer cancel()
	pin := []string{"GIT_DIR=" + os.DevNull}
	hooks := runListing(ctx, baseRepo, "", precursorEnv(true, pin)).hooks()
	cmd := exec.CommandContext(ctx, "git", hardenedProfileArgs(true, "rev-parse", "--absolute-git-dir")...)
	cmd.Dir = baseRepo
	cmd.Env = hardenedGitEnv(true, pin, hooks)
	_ = cmd.Run()
}

// statusPath is what the gate takes from `path`.
type statusPath struct {
	// probeTree is the --work-tree value of the `rev-parse --show-toplevel` call.
	probeTree string
	// spellings are the folders that the `gitdir` file of an entry can name.
	spellings []string
}

// resolvedDir is p with its symlinks resolved, or p cleaned when that fails. On
// Windows filepath.EvalSymlinks also gives each name its letter case on disk and its
// long form, and it keeps a junction and a `subst` drive.
func resolvedDir(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// statusPathOf runs the tests on `path` that come before the next git call. Each
// failure answers isRepo:false with 3 calls in total:
//
//   - `path` is not a directory: a regular file (row n09), or a folder that is gone
//     (row n26b). An empty `path` is the working directory of the daemon (rows n25d1
//     and n25d2 pass here and find no entry).
//   - `path` lies inside baseRepo and one of its components below baseRepo is a
//     symbolic link (rows n07 and n07L) or, on Windows, a junction (row n07-j). The
//     same layout with no link passes (rows n07b and n08). A link at the last
//     component fails too (row v4, Linux VM).
//
// The --work-tree value of the next call is `path` with its symlinks resolved (rows
// n06 and C12). When `path` lies inside baseRepo, it is baseRepo, resolved the same
// way (rows A04, A05a, N05a, N05b, n07b, n08 and K02b). A `path` that is baseRepo
// gives the same value both ways (row n18b).
//
// The `gitdir` file of an entry matches `path` as text in these spellings: as sent,
// with its symlinks resolved (rows n06, C12, D8, D13, D14dot and D14sp), and on
// Windows as the file system reports the open folder (rows D3, D4, D6 and D16: a
// `subst` drive, a junction and a `\\?\` path). A relative `path` counts from the
// working directory of the daemon (row n25c).
//
// For a `path` inside baseRepo the spellings are two: as sent, and the resolved
// baseRepo joined with the part of `path` below baseRepo as sent. On a Windows VM a
// `path` with 8.3 short names below a baseRepo in short names found no entry (row
// K02b), and a worktree outside baseRepo named in short names found its entry (row
// D13). Not measured: a `path` inside a baseRepo that lies below a symlink, where
// the second spelling is claustrum's choice, and another letter case below baseRepo
// on Windows.
func statusPathOf(path, baseRepo string) (statusPath, bool) {
	clean := filepath.Clean(path)
	if fi, err := os.Stat(clean); err != nil || !fi.IsDir() {
		return statusPath{}, false
	}
	absPath, err := filepath.Abs(clean)
	if err != nil {
		return statusPath{}, false
	}
	absBase, err := filepath.Abs(baseRepo)
	if err != nil {
		return statusPath{}, false
	}
	sp := statusPath{spellings: []string{absPath}}
	if pathStrictlyUnder(absPath, absBase) {
		if linkBelow(absBase, absPath) {
			return statusPath{}, false
		}
		sp.probeTree = resolvedDir(baseRepo)
		if rel, err := filepath.Rel(absBase, absPath); err == nil {
			if base, err := filepath.Abs(sp.probeTree); err == nil {
				sp.spellings = append(sp.spellings, filepath.Join(base, rel))
			}
		}
		return sp, true
	}
	sp.probeTree = resolvedDir(clean)
	for _, s := range []string{sp.probeTree, finalDirPath(clean)} {
		if abs, err := filepath.Abs(s); err == nil {
			sp.spellings = append(sp.spellings, abs)
		}
	}
	return sp, true
}

// linkBelow reports whether a component of p below base is a symbolic link or a
// junction. Go reports a junction as an irregular file. p lies strictly under base.
func linkBelow(base, p string) bool {
	rel, err := filepath.Rel(base, p)
	if err != nil {
		return true
	}
	cur := base
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		cur = filepath.Join(cur, part)
		if fi, err := os.Lstat(cur); err == nil && fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return true
		}
	}
	return false
}

// statusWorkTreeProbe runs the two calls in the common directory: `--git-dir=<common>
// config -z --list`, then the light `--git-dir=<common> --work-tree=<workTree>
// rev-parse --show-toplevel` (K1 calls 4 and 5). Both carry pin.
//
// A listing that fails is a refusal: its text is the hooks refusal, and `git version`
// runs after it (row o22, Linux VM). A `rev-parse` that fails answers isRepo:false
// (row n17). claustrum does not read the output of the `rev-parse`. What 89cb6289
// does with it is not measured.
func statusWorkTreeProbe(common, workTree string, pin []string) (ok bool, err error) {
	ctx, cancel := gitCtx()
	defer cancel()
	l := runListing(ctx, common, common, precursorEnv(false, pin))
	if l.err != nil {
		return false, statusRefusal(failedListingText(l, false))
	}
	cmd := exec.CommandContext(ctx, "git", hardenedProfileArgs(false,
		"--git-dir="+common, "--work-tree="+workTree, "rev-parse", "--show-toplevel")...)
	cmd.Dir = common
	cmd.Env = hardenedGitEnv(false, pin, l.hooks())
	return cmd.Run() == nil, nil
}

// errNotRegularFile is the error of an entry file that is not a regular file.
var errNotRegularFile = errors.New("not a regular file")

// openEntryFile opens name inside the entry folder that root holds. The open does not
// block, so a FIFO is judged at once and no read waits for a writer (rows n05a, C20a,
// C20b, n14b and o15b). The file must be a regular file. A symbolic link is followed
// only when it stays inside the entry folder: a relative link passes (rows n19e and
// o15a), an absolute one fails (row n19f).
func openEntryFile(root *os.Root, name string) (*os.File, fs.FileInfo, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|openNonBlocking, 0)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err == nil && !fi.Mode().IsRegular() {
		err = fmt.Errorf("%s: %w", name, errNotRegularFile)
	}
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	return f, fi, nil
}

// readEntryFile reads name inside the entry folder entry (openEntryFile). A file of
// more than max bytes is an error.
func readEntryFile(entry, name string, max int64) ([]byte, error) {
	root, err := os.OpenRoot(entry)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	f, _, err := openEntryFile(root, name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", name, max)
	}
	return b, nil
}

// entryPathValue is the path that an entry file holds: the content without the white
// space at both ends, counted from the entry folder when relative, and cleaned (rows
// n03, n19c and n19d). On Windows the cleaning turns each forward slash into a
// backslash (rows D11f and D11b).
func entryPathValue(entry string, content []byte) string {
	v := strings.TrimSpace(string(content))
	if filepath.IsAbs(v) {
		return filepath.Clean(v)
	}
	return filepath.Join(entry, v)
}

// statusMatchEntry finds the one entry under <common>/worktrees whose `gitdir` file
// names `path`. An entry is a real folder: a symlinked entry does not count (row
// n02), and a real folder that a symlink also points at counts once (row n02b). The
// last component of the value must be `.git` (row n04). The folder before it must
// equal one of the spellings of `path` as text. Another letter case or Unicode form
// does not match (rows C2, C3, C6, C9 and C10 on a macOS VM). Exactly one entry must
// match (rows n01, n18b and n25).
//
// On Windows the compare is by text too. Row D9 failed with the whole value in
// another letter case, `.git` included. A value with only one folder name in another
// case is not measured. An entry whose `gitdir` cannot be read is passed over: a
// second entry whose `gitdir` is a directory changes no answer (row v2, Linux VM).
func statusMatchEntry(common string, spellings []string) (string, bool) {
	dir := filepath.Join(common, "worktrees")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	found, n := "", 0
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		entry := filepath.Join(dir, e.Name())
		b, err := readEntryFile(entry, "gitdir", statusEntryFileMaxBytes)
		if err != nil {
			continue
		}
		v := entryPathValue(entry, b)
		if filepath.Base(v) != ".git" || !slices.Contains(spellings, filepath.Dir(v)) {
			continue
		}
		found = entry
		n++
	}
	return found, n == 1
}

// statusEntryNamesCommon judges the `commondir` file of the matched entry. Its value
// must equal the common directory as text, in the spelling that `rev-parse
// --absolute-git-dir` gave. A missing file fails (row n19a), and so do a FIFO (rows
// C20a and C20b), an absolute symlink (row n19f) and a file over the bound (row
// n19g). On macOS a value under /tmp fails, because git answers /private/tmp (row
// n19b.t). Another letter case fails (rows C11, D10 and W04), and so does a short
// name (rows K05a, K07a and K15a).
func statusEntryNamesCommon(entry, common string) bool {
	b, err := readEntryFile(entry, "commondir", statusEntryFileMaxBytes)
	if err != nil {
		return false
	}
	return entryPathValue(entry, b) == common
}

// statusHeadValid judges the `HEAD` file of the matched entry. 40 or 64 hex
// characters pass, and so does a `ref:` line under `refs/` (rows o14b, o14c, o14d and
// o14f, n16 and n16b). `garbage`, 39 hex characters and `ref: x` fail (rows n15, o14a
// and o14e). White space at both ends does not count (row o14f).
func statusHeadValid(b []byte) bool {
	s := strings.TrimSpace(string(b))
	if rest, ok := strings.CutPrefix(s, "ref:"); ok {
		return strings.HasPrefix(strings.TrimLeft(rest, " \t"), "refs/")
	}
	return isObjectID(s)
}

// statusIndexFiles tests the `index` and each `sharedindex.*` file of the entry
// before the temporary folder exists, and returns the `sharedindex.*` names. A file
// that is missing passes (row o15d). One that is not a regular file fails with no
// wait (row o15b, a FIFO), and so does one over statusIndexMaxBytes (rows o15c and
// o16). A symlink to a regular file inside the entry passes (row o15a).
func statusIndexFiles(entry string) ([]string, bool) {
	root, err := os.OpenRoot(entry)
	if err != nil {
		return nil, false
	}
	defer func() { _ = root.Close() }()
	ents, err := os.ReadDir(entry)
	if err != nil {
		return nil, false
	}
	var shared []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "sharedindex.") {
			shared = append(shared, e.Name())
		}
	}
	for _, name := range append([]string{"index"}, shared...) {
		fi, err := root.Stat(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || !fi.Mode().IsRegular() || fi.Size() > statusIndexMaxBytes {
			return nil, false
		}
	}
	return shared, true
}

// statusEntryConfig judges the `config.worktree` file of the matched entry. present
// is true when the entry has one. A missing file passes. A folder, a FIFO and a
// syntax error fail, with no wait on the FIFO (rows n14, n14b and n14c).
//
// The daemon lists the file with `git config --no-includes --file - --list -z`. The
// file is the stdin of that call, its working directory is that of the daemon, and it
// gets the environment of the daemon as it is (rows n12, n12b, n13 and n14c). Every
// key must be one of statusConfigKeys.
func statusEntryConfig(entry string) (present, ok bool) {
	if _, err := os.Lstat(filepath.Join(entry, "config.worktree")); errors.Is(err, fs.ErrNotExist) {
		return false, true
	}
	b, err := readEntryFile(entry, "config.worktree", statusEntryFileMaxBytes)
	if err != nil {
		return true, false
	}
	ctx, cancel := gitCtx()
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "config", "--no-includes", "--file", "-", "--list", "-z")
	cmd.Stdin = bytes.NewReader(b)
	out, err := cmd.Output()
	if err != nil {
		return true, false
	}
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		key, _, _ := bytes.Cut(rec, []byte{'\n'})
		if !slices.Contains(statusConfigKeys, string(key)) {
			return true, false
		}
	}
	return true, true
}

// statusEntry is the matched worktree entry after the gate.
type statusEntry struct {
	dir       string   // <common>/worktrees/<name>
	shared    []string // the sharedindex.* names
	hasConfig bool     // the entry holds a config.worktree
	reftable  bool     // the entry holds a reftable/tables.list
	tables    []string // the table names of that list
}

// statusEntryOf runs the entry part of the gate: the match, then `commondir`, `HEAD`,
// the index sizes and `config.worktree`. Each failure answers isRepo:false. The order
// of the tests after the match is not measured: each failing row broke one file only.
func statusEntryOf(common string, sp statusPath) (statusEntry, bool) {
	dir, ok := statusMatchEntry(common, sp.spellings)
	if !ok || !statusEntryNamesCommon(dir, common) {
		return statusEntry{}, false
	}
	head, err := readEntryFile(dir, "HEAD", statusEntryFileMaxBytes)
	if err != nil || !statusHeadValid(head) {
		return statusEntry{}, false
	}
	shared, ok := statusIndexFiles(dir)
	if !ok {
		return statusEntry{}, false
	}
	hasConfig, ok := statusEntryConfig(dir)
	if !ok {
		return statusEntry{}, false
	}
	reftable, tables, ok := statusReftable(dir)
	if !ok {
		return statusEntry{}, false
	}
	return statusEntry{dir: dir, shared: shared, hasConfig: hasConfig, reftable: reftable, tables: tables}, true
}

// statusReftable reads `reftable/tables.list` of the matched entry. A repository
// with the reftable format keeps the HEAD of a worktree there. present is true when
// the entry has that file. Each line names a table file in the `reftable` folder. A
// name whose file is missing answers isRepo:false (rows o19c and o19d, macOS VM).
//
// Not measured: a table that is not a regular file, a table over a size bound, and a
// name that is not a plain file name. claustrum answers isRepo:false for each, with
// statusIndexMaxBytes as the bound. The list itself is bounded by
// statusEntryFileMaxBytes, which is not measured either.
func statusReftable(entry string) (present bool, tables []string, ok bool) {
	const list = "reftable/tables.list"
	if _, err := os.Lstat(filepath.Join(entry, filepath.FromSlash(list))); errors.Is(err, fs.ErrNotExist) {
		return false, nil, true
	}
	b, err := readEntryFile(entry, list, statusEntryFileMaxBytes)
	if err != nil {
		return true, nil, false
	}
	root, err := os.OpenRoot(entry)
	if err != nil {
		return true, nil, false
	}
	defer func() { _ = root.Close() }()
	for _, name := range strings.Fields(string(b)) {
		if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
			return true, nil, false
		}
		fi, err := root.Stat("reftable/" + name)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() > statusIndexMaxBytes {
			return true, nil, false
		}
		tables = append(tables, name)
	}
	return true, tables, true
}

// copyStatusFile, writeStatusFile and chtimesStatusFile are seams over the file
// system calls that fill the temporary git folder. The write and the Chtimes target
// a folder and a file that the daemon just made, which no fixture can make fail.
// Production never reassigns them.
var (
	copyStatusFile    = copyEntryFile
	writeStatusFile   = os.WriteFile
	chtimesStatusFile = os.Chtimes
)

// copyEntryFile copies name of the entry folder that root holds to dst, and gives dst
// the modification time of the source. For the index that time matters: git compares
// the time of each work-tree file with the time of the index. A newer index makes git
// trust its stat data and miss a change that keeps the size of a file. A source of
// more than max bytes is an error.
func copyEntryFile(root *os.Root, name, dst string, max int64) error {
	src, fi, err := openEntryFile(root, name)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(src, max+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > max {
		err = fmt.Errorf("%s is larger than %d bytes", name, max)
	}
	if err != nil {
		return err
	}
	return chtimesStatusFile(dst, fi.ModTime(), fi.ModTime())
}

// buildStatusGitDir makes the temporary git folder of one request and returns its
// path. It holds copies of `HEAD` and `index` of the entry and a `commondir` file
// that names the common directory (row K1). It also holds `config.worktree` when the
// entry has one (rows n12 and o17), `info/sparse-checkout` (row o17) and each
// `sharedindex.*` file (rows o16b1 and o16b2). For a reftable repository it holds
// `reftable/tables.list` and each table that the list names (rows o19a and o19b, macOS
// VM). In row o19d 89cb6289 also copied a table that the list did not name, before it
// answered isRepo:false. Whether it copies such a table in a request that passes is
// not measured. claustrum does not. A file that the entry does not hold is left out
// (row o15d). Any other failure is returned. The caller removes the folder.
//
// git needs the `commondir` file to resolve a branch HEAD against the refs of the
// repository. GIT_COMMON_DIR alone is not enough for that.
func buildStatusGitDir(e statusEntry, common string) (string, error) {
	tmp, err := os.MkdirTemp("", statusGitDirTempPrefix)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(e.dir)
	if err != nil {
		return tmp, err
	}
	defer func() { _ = root.Close() }()
	type item struct {
		name string
		max  int64
	}
	items := []item{{"HEAD", statusEntryFileMaxBytes}, {"index", statusIndexMaxBytes}}
	for _, s := range e.shared {
		items = append(items, item{s, statusIndexMaxBytes})
	}
	if e.hasConfig {
		items = append(items, item{"config.worktree", statusEntryFileMaxBytes})
	}
	if _, err := root.Stat("info/sparse-checkout"); err == nil {
		items = append(items, item{"info/sparse-checkout", statusIndexMaxBytes})
	}
	if e.reftable {
		items = append(items, item{"reftable/tables.list", statusEntryFileMaxBytes})
		for _, name := range e.tables {
			items = append(items, item{"reftable/" + name, statusIndexMaxBytes})
		}
	}
	for _, it := range items {
		dst := filepath.Join(tmp, filepath.FromSlash(it.name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return tmp, err
		}
		if err := copyStatusFile(root, it.name, dst, it.max); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return tmp, err
		}
	}
	if err := writeStatusFile(filepath.Join(tmp, "commondir"), []byte(common+"\n"), 0o600); err != nil {
		return tmp, err
	}
	return tmp, nil
}

// statusRun runs the git commands of one answer.
type statusRun struct {
	ctx    context.Context
	path   string   // as sent: the working directory and the --work-tree value
	common string   // the common directory
	tmp    string   // the temporary git folder
	pin    []string // GIT_COMMON_DIR=<common>, on the calls in the temporary folder
	// commonPin is the pin of the calls with --git-dir=<common> (statusCommonPin).
	commonPin  []string
	attrSource bool // git takes --attr-source (attrSourceArgs)
}

// statusCommonPin is the GIT_COMMON_DIR pin of the calls that carry
// --git-dir=<common>: K1 calls 4 and 5, and the two calls of emptyTree. When the
// daemon's own environment sets GIT_COMMON_DIR, those calls keep the daemon's value
// and get no pin. The calls in the temporary folder always get
// GIT_COMMON_DIR=<common> (rows E07a and E07b, Linux and macOS VMs). No other git
// variable was in the daemon's environment in those rows.
func statusCommonPin(common string) []string {
	if daemonCommonDirSet() {
		return nil
	}
	return []string{"GIT_COMMON_DIR=" + common}
}

// emptyTree runs the two calls that give the id of the empty tree: `--git-dir=<common>
// config -z --list`, then the heavy `--git-dir=<common> hash-object -t tree <null
// device>`. Both run in the working directory of the daemon (K1 calls 7 and 8). A
// `hash-object` that fails changes no answer (row n17f): the id is then gitEmptyTree.
// A listing that fails here is not measured. claustrum goes on with the two base pins.
func (r *statusRun) emptyTree() string {
	hooks := runListing(r.ctx, "", r.common, precursorEnv(true, r.commonPin)).hooks()
	cmd := exec.CommandContext(r.ctx, "git", hardenedProfileArgs(true,
		"--git-dir="+r.common, "hash-object", "-t", "tree", os.DevNull)...)
	cmd.Env = hardenedGitEnv(true, r.commonPin, hooks)
	out, err := cmd.Output()
	if id := strings.TrimSpace(string(out)); err == nil && isObjectID(id) {
		return id
	}
	return gitEmptyTree
}

// isObjectID reports whether s is 40 or 64 hex characters.
func isObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := range len(s) {
		if !isHexDigit(s[i]) {
			return false
		}
	}
	return true
}

// git runs one command of the answer. Three calls come before it: the two of
// emptyTree, then `--git-dir=<temp> config -z --list` with `path` as its working
// directory (K1 calls 7 to 9). The command runs in `path` with the --attr-source
// option, the heavy profile, --git-dir=<temp> and --work-tree=<path as sent>. noLocks
// adds GIT_OPTIONAL_LOCKS=0: without it a `git status` rewrites the index of the
// worktree. excludes is the core.excludesFile value.
//
// The --attr-source value is the id that emptyTree gave right before. In a SHA-256
// repository it has 64 hex characters (rows v6a and v6b, Linux VM). The probe of
// attrSourceArgs keeps the SHA-1 id there.
//
// A failing listing in the temporary folder is a refusal with the hooks text. That
// follows row o22, where the listing with --git-dir=<common> failed. A failure of this
// one listing is not measured.
func (r *statusRun) git(noLocks bool, excludes string, args ...string) ([]byte, error) {
	var attr []string
	if id := r.emptyTree(); r.attrSource {
		attr = []string{"--attr-source=" + id}
	}
	l := runListing(r.ctx, r.path, r.tmp, precursorEnv(true, r.pin))
	if l.err != nil {
		return nil, statusRefusal(failedListingText(l, true))
	}
	full := append(attr, profileArgsWithExcludes(true, excludes,
		append([]string{"--git-dir=" + r.tmp, "--work-tree=" + r.path}, args...)...)...)
	cmd := exec.CommandContext(r.ctx, "git", full...)
	cmd.Dir = r.path
	cmd.Env = hardenedGitEnv(true, r.pin, l.hooks())
	if noLocks {
		cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0")
	}
	return cmd.Output()
}

// statusChanges builds `changes` for the worktree at path, whose entry passed the
// gate. It runs four commands in this order, each with cwd `path` (K1 calls 10, 14,
// 18 and 22):
//
//	status --porcelain --untracked-files=all --ignore-submodules=all
//	ls-files -s -z
//	rev-parse --verify -q HEAD^{commit}
//	diff-index --cached --raw -z --ignore-submodules=none <HEAD or the empty tree id> --
//
// A `status`, `ls-files` or `diff-index` that fails gives the exec error, for example
// "exit status 1" (rows n17c, n17d and n17e). When the `rev-parse` fails, HEAD names
// no commit. The daemon then runs the two calls of emptyTree once more and passes
// that id to `diff-index` (rows n16, o13 and o14d).
//
// The status runs in a temporary git folder, so it never writes the index of the
// worktree. On Windows the three commands with GIT_OPTIONAL_LOCKS=0 get
// statusExcludesFile as their core.excludesFile value. That is the divergence D16.
func statusChanges(path, common string, e statusEntry) ([]string, error) {
	attrSource := attrSourceArgs() != nil
	tmp, err := buildStatusGitDir(e, common)
	if tmp != "" {
		defer func() { _ = os.RemoveAll(tmp) }()
	}
	if err != nil {
		return nil, err
	}
	ctx, cancel := gitCtx()
	defer cancel()
	r := &statusRun{ctx: ctx, path: path, common: common, tmp: tmp,
		pin: []string{"GIT_COMMON_DIR=" + common}, commonPin: statusCommonPin(common), attrSource: attrSource}
	excludes := statusExcludesFile()

	porcelain, err := r.git(true, excludes, "status", "--porcelain",
		"--untracked-files=all", "--ignore-submodules=all")
	if err != nil {
		return nil, err
	}
	index, err := r.git(true, excludes, "ls-files", "-s", "-z")
	if err != nil {
		return nil, err
	}
	base := "HEAD"
	if _, err := r.git(false, userExcludesFile(), "rev-parse", "--verify", "-q", "HEAD^{commit}"); err != nil {
		var refusal statusRefusal
		if errors.As(err, &refusal) {
			return nil, err
		}
		base = r.emptyTree()
	}
	staged, err := r.git(true, excludes, "diff-index", "--cached", "--raw", "-z",
		"--ignore-submodules=none", base, "--")
	if err != nil {
		return nil, err
	}

	changes := porcelainEntries(porcelain)
	subs := append(presentSubmodules(path, index), stagedSubmodules(staged)...)
	if len(subs) > statusMaxSubmoduleEntries {
		rest := len(subs) - statusMaxSubmoduleEntries
		subs = append(subs[:statusMaxSubmoduleEntries],
			submoduleOverflowStart+strconv.Itoa(rest)+submoduleOverflowEnd)
	}
	return append(changes, subs...), nil
}

// porcelainEntries splits the output of `status --porcelain` into entries. Each line
// passes as it is, with its leading space: the XY column is positional, so the space
// of " M f" is data (rows K1, n21 and o25). An entry longer than statusLineMaxBytes
// is cut to that many bytes of the whole line, and "…" follows (row o1). At most
// statusMaxLines entries pass, and no entry marks the cut (row o2).
func porcelainEntries(out []byte) []string {
	var entries []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if len(entries) == statusMaxLines {
			break
		}
		t := strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(t) == "" {
			continue
		}
		if len(t) > statusLineMaxBytes {
			t = t[:statusLineMaxBytes] + "…"
		}
		entries = append(entries, t)
	}
	return entries
}

// submoduleEntry is one submodule entry: " S ", the quoted name and text. A name
// longer than statusNameMaxBytes is cut to that many bytes, and "…" follows. The
// quoting is that of Go: double quotes, `\"`, `\n`, `\t`, and `\xff` for a byte that
// is not valid UTF-8. A printable character outside ASCII stays as it is (row o11).
func submoduleEntry(name, text string) string {
	if len(name) > statusNameMaxBytes {
		name = name[:statusNameMaxBytes] + "…"
	}
	return " S " + strconv.Quote(name) + text
}

// presentSubmodules makes the entries from `ls-files -s -z`. A gitlink is an index
// entry of mode 160000. For each one the daemon looks at <name>/.git inside the work
// tree, and follows no link out of the work tree:
//
//   - Not there: no entry (rows n20e and o6d).
//   - There, as a file, a folder or a dangling symlink: the "present" text (rows n20,
//     o6a, o6b and o6c). A gitlink path that is a symlink to a folder inside the work
//     tree counts as present (row o5b).
//   - It cannot be looked at: the "could not be inspected" text. Measured for a
//     gitlink path that is a regular file, a symlink to a folder outside the work
//     tree, and a folder of mode 000 (rows o4, o5 and o5c).
//
// There is one entry for each index stage, so a conflicted gitlink gives three (row
// o12).
func presentSubmodules(workTree string, lsFiles []byte) []string {
	var entries []string
	root, rootErr := os.OpenRoot(workTree)
	if rootErr == nil {
		defer func() { _ = root.Close() }()
	}
	for _, rec := range bytes.Split(lsFiles, []byte{0}) {
		meta, name, ok := bytes.Cut(rec, []byte{'\t'})
		if !ok || !bytes.HasPrefix(meta, []byte(gitlinkMode+" ")) {
			continue
		}
		err := rootErr
		if err == nil {
			_, err = root.Lstat(string(name) + "/.git")
		}
		switch {
		case err == nil:
			entries = append(entries, submoduleEntry(string(name), submodulePresentText))
		case !errors.Is(err, fs.ErrNotExist):
			entries = append(entries, submoduleEntry(string(name), submoduleUnreadText))
		}
	}
	return entries
}

// stagedSubmodules makes the entries from `diff-index --cached --raw -z`. Each record
// is ":<old mode> <new mode> <old id> <new id> <status>", a NUL and the path. A record
// whose old or new mode is 160000 gives the "change staged" text (rows o7, o8, o9,
// o9b, o9c and o13). A rename or copy record has two paths. The command asks for no
// rename detection, so no row has one, and claustrum names the first path.
func stagedSubmodules(diffIndex []byte) []string {
	var entries []string
	toks := bytes.Split(diffIndex, []byte{0})
	for i := 0; i+1 < len(toks); i++ {
		meta, ok := bytes.CutPrefix(toks[i], []byte(":"))
		if !ok {
			continue
		}
		f := strings.Fields(string(meta))
		if len(f) < 5 {
			continue
		}
		i++
		name := string(toks[i])
		if f[4][0] == 'R' || f[4][0] == 'C' {
			i++
		}
		if f[0] == gitlinkMode || f[1] == gitlinkMode {
			entries = append(entries, submoduleEntry(name, submoduleStagedText))
		}
	}
	return entries
}
