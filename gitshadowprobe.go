package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The call that asks git itself about a repository, as 5fd08069 makes it in the
// state shadow. Before the daemon reads the config of a repository and uses its
// folder for it, it runs `git rev-parse --git-dir` in the root of the work tree.
// That call names no copy of a repository: its common directory is the folder
// `.probe` in the folder of the daemon. So git judges the repository as it
// stands, with the owner test of git and with no setting of the daemon's copy.
//
// A request with "shadowDirOff":true, the state fallback-git and a server with
// no run folder make no such call (s3-linux cell B1 and cell 29 of
// s3p2b-edges-linux2, Linux VM).

// shadowProbeName is the folder in the folder of the daemon that the call names
// as its common directory. probeConfigText is its config. 5fd08069 has there
// HEAD (25 bytes, the text of shadowTestFirstHead), config (36 bytes) and the
// empty folders info, objects and refs, with equal texts on Linux, macOS and
// Windows VMs (s3-linux cell A3, s3-macos cell A3, s3p2-windows cell B3). On
// Linux and macOS every folder below the folder of the daemon has mode 0700 and
// every file 0600 (the same cells).
const (
	shadowProbeName = ".probe"
	probeConfigText = "[core]\n\trepositoryformatversion = 0\n"
)

// The times of the call. Each is a variable, so that a test can lower it.
var (
	// probeKeep is how long a good answer is kept, from the END of the call.
	// On 5fd08069 a request 4.90 s after the start of a short call made no
	// call, and one at 4.95 s made it at 5.02 s. After a call that its wrapper
	// held for 2 s, a request 4.50 s after its end made no call and one 5.50 s
	// after its end made one (cell 34 of s3p2b-edges-linux2, Linux VM). On a
	// Windows VM no call came 1.04 s after the end and one came 5.60 s after
	// it, and on a macOS VM none at 2.84 s and one at 8.9 s (cell 25 of
	// s3p2b-edges2-windows, cell 40 of s3p2b-edges-macos).
	probeKeep = 5 * time.Second
	// probeCallBound ends the call. shadowCallDrain is the wait for its output
	// pipes after that. On 5fd08069 a call held for 29 s gave a normal reply at
	// 29.30 s. A call held for 31 s or 40 s gave the error at 31.16 s and 31.15
	// s. The sleep child of the wrapper lived on in that round (cell 37 of
	// s3p2b-edges-linux2, Linux VM). A macOS VM gave the error at 30.21 s and a
	// Windows VM 29.98 s after the start of the call (cell 40 of
	// s3p2b-edges-macos and of s3p2b-edges-windows). The split into the two
	// times is fitted, as for a call of the start test.
	probeCallBound = 30 * time.Second
	// probeClock is the clock of the kept answer.
	probeClock = time.Now
)

// keptProbe is one good answer: the end of its call, and what Stat gave for
// the root of the work tree and for its `.git` entry right after the call.
type keptProbe struct {
	end       time.Time
	root, git os.FileInfo
}

// probeStat is the Stat of path with its file identity read now, or nil. On
// Windows os.SameFile reads the identity of a FileInfo from the path at the
// first compare, so a later compare with a new folder at that path found no
// difference.
func probeStat(path string) os.FileInfo {
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	os.SameFile(fi, fi)
	return fi
}

// probeGitStat is probeStat for the `.git` entry at path. A junction there is
// read as the entry itself, not as the folder behind it.
//
// The rule is fitted to part Q of val-s3p2b2-9-windows (Windows VM, 2 forms, 2
// of 2 runs). `repo\.git` is a junction before the daemon starts. The first
// request of 5fd08069 makes the call about the repository, and its seven later
// requests make no git call. The driver runs a plain `git status` in the
// repository between two requests. With a real `.git` directory and the same
// driver, each request of 5fd08069 makes the call again (control cell Qk). The
// time and the identity of the junction entry are the same at every view of
// the cell. On Windows Lstat gives ModeIrregular for a junction. A symbolic
// link keeps the Stat rule, on every system. On 5fd08069 later requests make
// no call with a directory symbolic link too (part D of
// val-s3p2b2-12-windows). claustrum makes one call for each later request
// with a link to a real git directory, and none with a link to the copy T of
// cell DT-symd-late-to-T (section 1.8 of val-s3p2b2-13-windows, Windows VM, 2
// of 2 runs).
func probeGitStat(path string) os.FileInfo {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeIrregular != 0 {
		os.SameFile(fi, fi)
		return fi
	}
	return probeStat(path)
}

// sameProbeFile reports whether a and b are one file with one modification
// time. Two paths with no file are equal. The mode and the owner do not count
// (keptProbe.fresh).
func sameProbeFile(a, b os.FileInfo) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return os.SameFile(a, b) && a.ModTime().Equal(b.ModTime())
}

// fresh reports whether the answer still stands for root: it is younger than
// probeKeep, and the root of the work tree and its `.git` entry each have the
// identity and the modification time that they had after the call.
//
// The rule is fitted to these rows of 5fd08069, each one change 0.1 s after a
// git.info and a second git.info 1.0 s after the call (cell 35 of
// s3p2b-edges-linux2, Linux VM. Cell 25 of s3p2b-edges2-macos and of
// s3p2b-edges2-windows). A new call: `touch <repo>`, `mkdir <repo>/x`, `touch
// <repo>/.git`, a new file in `.git`, and on Linux and macOS a modification
// time of `.git` or of `<repo>` that is set 100 s back. A new call on Linux:
// `.git` replaced by a copy with both old modification times. No new call: a
// new text of `.git/config`, `touch .git/config`, HEAD rewritten in place,
// `touch .git/HEAD`, `touch .git/objects`, `touch <repo>/.claude`, a touch of
// the parent folder, one more worktree by plain git, `chmod 700 .git` and
// `chown nobody:nogroup .git`. The last six ran on Linux only. After that
// change of the owner the answer stays kept, and 5fd08069 answers normally
// until the time is over (cell 31 of s3p2b-edges2-linux, Linux VM).
func (k keptProbe) fresh(root string) bool {
	return !k.end.IsZero() && probeClock().Sub(k.end) < probeKeep &&
		sameProbeFile(k.root, probeStat(root)) && sameProbeFile(k.git, probeGitStat(filepath.Join(root, ".git")))
}

// repoAnswer is what the daemon makes of the answer of git. The zero value is a
// good answer. noRepo is the answer "this is no repository that git uses", and
// text is the text of an error.
type repoAnswer struct {
	noRepo bool
	text   string
}

func (a repoAnswer) bad() bool {
	return a.noRepo || a.text != ""
}

// probeLeftovers are the entries of `.probe` that each preparation deletes as a
// tree when they are there. After an uncached call about the repository,
// 5fd08069 had removed `info/attributes`, `info/exclude`, `shallow`,
// `BISECT_LOG`, a folder `worktrees` with a file in it and a folder `reftable`
// (measured on Linux). The other names are claustrum's own, not measured.
var probeLeftovers = []string{
	"info/attributes", "info/exclude", "shallow", "worktrees", "reftable",
	"rebase-merge/head-name", "rebase-apply/head-name", "rebase-apply/applying",
	"BISECT_LOG", "BISECT_START",
}

// probeRebaseFolders go after probeLeftovers with one plain remove each. So
// only an empty folder or a file goes, and a folder with content stays
// (claustrum's own, not measured).
var probeRebaseFolders = []string{"rebase-merge", "rebase-apply"}

// prepareProbeFolder makes the folder `.probe` in dir, the folder of the
// daemon, with its two files and its three folders. It deletes the entries of
// probeLeftovers and probeRebaseFolders in it. Every other entry stays
// (claustrum's own, not measured). A planted `keepme` stayed on 5fd08069 (measured on
// Linux). The files are new
// files through a temporary file, as the copies of a repository: a poll on a
// macOS VM saw `.probe/.tmp-<digits>` (val-s3p2b2-3-macos row D7). A file that
// holds its text already stays as it is. An unchanged config kept its inode
// and its time on 5fd08069, and a folder at HEAD became the file (measured on
// Linux). A failed temporary file goes.
//
// 5fd08069 has the folder after a first good request (s3-linux cell A3). It has
// none in the state fallback-git and after a failed test, where it makes no such
// call (s3p2-oldgit section 5). A folder of the daemon that it made again after
// the first one went had none after one git.status that answered normally
// (s3-linux cell B6, Linux VM, and s3-macos cell B6). claustrum prepares the
// folder right before each call (askGit). That fits those rows when the
// git.status of cell B6 came inside the kept time of the answer, which the cell
// does not record. Git needs the folder: without it the call answers "not a git
// repository" (git 2.47.3 on the development host).
//
// Each delete goes through removeEntry or removeOneEntry, with fixed names
// below the folder of the daemon. An error names its full path
// (probePathError).
func (h *shadowHost) prepareProbeFolder(dir string) error {
	probe := filepath.Join(dir, shadowProbeName)
	rel := filepath.Join(filepath.Base(dir), shadowProbeName)
	base, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = base.Close() }()
	if err := base.Mkdir(shadowProbeName, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return probePathError(dir, err)
	}
	root, err := base.OpenRoot(shadowProbeName)
	if err != nil {
		return probePathError(dir, err)
	}
	defer func() { _ = root.Close() }()
	// The order is `refs`, `objects`, `info`, then `config`, then `HEAD`. On
	// 5fd08069 the birth times of the entries have that order in 14 of 14
	// folders (row E2 of val-s3p2b2-5-macos, macOS VM). The write times of a
	// Windows VM fit it: `objects` and `refs`, then `config` and `info`, then
	// `HEAD` (val-s3p2b2-5-windows section 2, one polled run).
	for _, name := range []string{"refs", "objects", "info"} {
		if err := root.Mkdir(name, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return probePathError(probe, err)
		}
	}
	for _, name := range probeLeftovers {
		if err := h.removeEntry(filepath.Join(rel, filepath.FromSlash(name))); err != nil {
			return probePathError(filepath.Dir(dir), err)
		}
	}
	for _, f := range []struct{ name, text string }{{"config", probeConfigText}, {"HEAD", shadowTestFirstHead}} {
		if err := h.writeProbeFile(root, rel, f.name, f.text); err != nil {
			return probePathError(probe, err)
		}
	}
	for _, name := range probeRebaseFolders {
		_ = h.removeOneEntry(filepath.Join(rel, name))
	}
	return nil
}

// writeProbeFile writes text to name in root, the folder `.probe` at rel below
// the folder of all daemons, unless the file holds text already. A folder at
// name goes first as a tree. A temporary file that a failed write left goes.
func (h *shadowHost) writeProbeFile(root *os.Root, rel, name, text string) error {
	if got, err := root.ReadFile(name); err == nil && string(got) == text {
		return nil
	}
	if fi, err := root.Lstat(name); err == nil && fi.IsDir() {
		if err := h.removeEntry(filepath.Join(rel, name)); err != nil {
			return err
		}
	}
	left, err := writeNewFile(root, name, []byte(text))
	if left != "" {
		_ = h.removeEntry(filepath.Join(rel, left))
	}
	return err
}

// probePathError gives a path error of a root at dir the full path, and the
// name of the call without the `at` of the root. For a temporary file that
// cannot be made, 5fd08069 writes `open <.probe path>/.tmp-<n>: permission
// denied` (measured on Linux). The text of the other calls is claustrum's own,
// not measured.
func probePathError(dir string, err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) && !filepath.IsAbs(pe.Path) {
		return &fs.PathError{Op: strings.TrimSuffix(pe.Op, "at"), Path: filepath.Join(dir, pe.Path), Err: pe.Err}
	}
	return err
}

// ensureProbeFolder prepares the folder `.probe` in folder (prepareProbeFolder)
// and returns the refusal text of a failed step, or "". The step runs before
// each call about the repository that no kept answer spares. 5fd08069 has the
// 36 bytes of the config again after a git.info that came 6 s after the file
// was emptied (cell C10 of round tier0-5, Linux VM). When `.probe` cannot be
// prepared, 5fd08069 answers -32603 with this text and runs no git (measured on
// Linux). It holds probeFolderMu from the look to the end of the make.
func (h *shadowHost) ensureProbeFolder(folder string) string {
	h.probeFolderMu.Lock()
	defer h.probeFolderMu.Unlock()
	if err := h.prepareProbeFolder(folder); err != nil {
		return fmt.Sprintf("Claude could not prepare its own files on the remote machine: cannot write %q: %v", filepath.Join(folder, shadowProbeName), err)
	}
	return ""
}

// probeNames are the variables that the call sets. The daemon's own entries of
// these names do not go to the call. 5fd08069 replaces the daemon's
// GIT_CEILING_DIRECTORIES for this call (s3p2-linux2 cell D5, Linux VM). The
// call has no GIT_DIR and no GIT_WORK_TREE in the cells, where the daemon has
// none.
var probeNames = []string{
	"GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_CEILING_DIRECTORIES",
	"GIT_ALLOW_PROTOCOL", "GIT_NO_LAZY_FETCH", "GIT_TERMINAL_PROMPT", "LC_ALL", "LANGUAGE",
}

// probeDropNames are the daemon's variables that the call does not get. The
// call keeps the daemon's GIT_CONFIG_COUNT and its pairs. 5fd08069 does both on
// Linux, macOS and Windows VMs (cells C8, C8b, C8c and C8K of round tier3-02).
// The later calls of a request get these four names. Every GIT_CONFIG_KEY_<n>
// and GIT_CONFIG_VALUE_<n> of the daemon passes, also one outside the count.
// The hardened calls rebuild only the counted pairs (configPinEnv). Cell C8c
// set a count of 1 with pair 0 only, so a name outside the count is not
// measured.
var probeDropNames = []string{
	"GIT_NAMESPACE", "GIT_INDEX_FILE", "GIT_DISCOVERY_ACROSS_FILESYSTEM", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
}

// probeEnv is the environment of the call for the work tree root in the folder
// dir of the daemon. The eight variables and their order are those of the call
// log of 5fd08069 on a Linux VM (s3p2-linux cell B3a and val-s3p2b1-linux cell
// A1). The ceiling is the parent folder of the root: `<W>/home` for
// `<W>/home/repo`, and `<repo>/.claude/worktrees` for a linked worktree there
// (s3p2-linux cells B3a and B3b). A Windows VM has `C:\o\w` for `C:\o\w\repo`,
// and a macOS VM `/private/tmp/o` (cell 40 of s3p2b-edges-windows, cell 50 of
// s3p2b-edges-macos). A root with no parent folder is not measured.
func probeEnv(dir, root string) []string {
	env := slices.DeleteFunc(listingEnvBase(), func(kv string) bool {
		return slices.Contains(probeNames, envName(kv)) || slices.Contains(probeDropNames, envName(kv))
	})
	probe := filepath.Join(dir, shadowProbeName)
	return append(env,
		"GIT_COMMON_DIR="+probe,
		"GIT_OBJECT_DIRECTORY="+filepath.Join(probe, "objects"),
		"GIT_CEILING_DIRECTORIES="+filepath.Dir(root),
		"GIT_ALLOW_PROTOCOL=denied_by_claude_ssh",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
		"LANGUAGE=C",
	)
}

// probeArgs is the argv of the call. On a Windows VM the first item is
// core.hooksPath=NUL (s3p2-linux2 cell D4, Linux VM. s3p2-windows cells D4 and
// B3a, Windows VM. Cell 50 of s3p2b-edges-macos). The empty core.fsmonitor=
// follows c8e2f083 (measured on Windows).
func probeArgs() []string {
	return []string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=", "rev-parse", "--git-dir"}
}

// askGit asks git about the repository of the trust verdict t for a request on
// dir, when no good answer is kept for its work tree root.
//
// The call runs in the root of the work tree. For a folder inside the work
// tree that is the root of the repository, and for a linked worktree it is the
// worktree (s3p2-linux2 cell D7b and s3p2-linux cell B3b, Linux VM). A bare
// repository gets the call in the repository itself (s3p2-linux cell B3c). A
// folder inside a git directory is not measured: the call runs in the git
// directory.
//
// The answers, as 5fd08069 reads them (cell 39 of s3p2b-edges-linux2, Linux
// VM, where a wrapper answers the call itself):
//
//   - Exit 0 and the git directory of the repository: good. Git prints `.git`,
//     and the full path is good too. A path that is not absolute is taken from
//     the root.
//   - Exit 128 with a no-repository text (probeSaysNoRepo): "no repository".
//     The cells have
//     the "dubious ownership" text of git for a repository of another owner
//     and `fatal: not a git repository (or any of the parent directories):
//     .git` (cells D1 and D3 of s3p2-linux2, s3p2-macos and s3p2-windows).
//   - Every other failure that probeCallBound did not stop: the error `git could not check this
//     repository on the remote machine: at "<root>": exit status <n>`, after
//     one `git version` call (cannotRunText). If that call fails, its text is
//     the answer (cell P14x of round task073-03, Linux VM). After `fatal: bad
//     config line 3 in file <home>/.gitconfig` that holds for each of the five
//     methods (cell E8 of round task083-01 and cells E8s, E8l, E8c and E8r of
//     round task083-02, Linux VM). Exit 128 with
//     another text has the rule of exit 1 with a text (cell P8 of round
//     task073-03, cells Q5 to Q7 of round task073-05, Linux VM, and cell W2-268
//     of round task073-01, Windows VM). Exit 1 with a text on the
//     error output has that text in the place of the exec error: `… at
//     "<root>": fatal: text of the wrapper for p3`, with the same call count
//     (cells p3 of val-s3p2b2-7-linux2, Linux VM). A text of two lines stands
//     there with one space between the lines (shadowRun.allLines, cell N-f of
//     val-s3p2b2-8-linux2, Linux VM). Exit 2 with a text has the rule of exit 1,
//     with 13 and 2 calls (the same cell). A SIGTERM (cell P13 of round
//     task073-03) and a git that cannot start (cell W2-280 of round task073-01)
//     have it too. Another exit code is not measured: it has the rule of exit 1.
//   - Exit 0 and another path, or no output: the call runs one more time. The
//     same answer then gives the error `git and Claude disagree about which
//     repository holds this folder: …`. With no output the path of git is the
//     root.
//   - A call that probeCallBound ended: the error `… it did not finish within
//     30 seconds in "<root>"; try again` (cell 37 of that round, and cell 40 on
//     macOS and Windows VMs).
//   - Exit 0 while a child keeps the output pipes past shadowCallDrain: the
//     output read so far is the answer, as above, with no `git version` call.
//     5fd08069 goes on about 1 s after the exit (cells P1 to P3 of round
//     task100-01, Linux VM, git.status). The rest is not measured.
//
// Only a good answer is kept (keptProbe). After a bad answer each request asks
// again (cells 37 and 39). On Windows git prints the path with forward slashes.
// A path of git that names the same folder in another spelling counts as the
// git directory (not measured).
//
// The "disagree" text shows the path of git as git printed it. For the answer
// `C:/o/w/other/.git` 5fd08069 writes `git finds "C:/o/w/other/.git"`. For
// the same path with backslashes it writes the backslashes (table P2 of
// val-s3p2b2-7-windows, Windows VM). The paths of the root and of the daemon
// keep their backslashes in both rows. A printed path that is not clean, or
// not absolute, is not measured: it is shown cleaned and from the root.
func (h *shadowHost) askGit(t gitDirTrust, dir string) repoAnswer {
	root, _ := gitWalkRoot(dir)
	if root == "" {
		root = t.pinCommonDir
	}
	want := t.pinCommonDir
	if t.worktreeEntry != "" {
		want = t.worktreeEntry
	}
	// The lock is not held while git runs, so a slow call on one repository
	// does not hold a request on another one.
	h.probeMu.Lock()
	kept := h.probes[root]
	h.probeMu.Unlock()
	if kept.fresh(root) {
		return repoAnswer{}
	}
	h.mu.Lock()
	folder := h.dir
	h.mu.Unlock()
	if folder == "" {
		return repoAnswer{}
	}
	if text := h.ensureProbeFolder(folder); text != "" {
		return repoAnswer{text: text}
	}
	env := probeEnv(folder, root)
	found, shown := "", ""
	for try := 0; try < 2; try++ {
		r := exitedInTime(boundedGit(probeCallBound, root, env, probeArgs()...))
		if r.ended {
			return repoAnswer{text: fmt.Sprintf("git could not check this repository on the remote machine: it did not finish within 30 seconds in %q; try again", root)}
		}
		if r.err != nil {
			if probeSaysNoRepo(r) {
				return repoAnswer{noRepo: true}
			}
			if whole := cannotRunText(); whole != "" {
				return repoAnswer{text: whole}
			}
			return repoAnswer{text: repoCheckFailedText(root, r.allLines())}
		}
		found = filepath.FromSlash(r.out)
		if !filepath.IsAbs(found) {
			found = filepath.Join(root, found)
		}
		found = filepath.Clean(found)
		shown = printedProbePath(r.out, found, string(filepath.Separator))
		if found == filepath.Clean(want) || sameProbeFolder(found, want) {
			h.keepProbe(root)
			return repoAnswer{}
		}
	}
	return repoAnswer{text: fmt.Sprintf("git and Claude disagree about which repository holds this folder: from %q git finds %q, Claude finds %q; try again", root, shown, want)}
}

// probeSaysNoRepo reports whether a call about the repository that exits 128
// names no repository by its text, so that no `git version` call follows.
// Round task073-03 (Linux VM) has these texts of 5fd08069. A text that starts
// with `fatal: not a git repository`, in any letter case (cells P6 and P7).
// `detected dubious ownership` after a first line (cell P9). `fatal: cannot use
// bare repository 'x'` (cell P10). The same text after a first line, as
// `warning: w`, does not count (cell P8). The text of git 2.56 for a `.git` file
// to nowhere counts too (claustrum's own, not measured).
//
// The two phrases count at any place in the text, in any letter case. That is
// claustrum's own, not measured beyond the cells. The phrase `unsafe
// repository` of older gits counts the same way (claustrum's own, not measured).
func probeSaysNoRepo(r shadowRun) bool {
	l := listingRun{stderr: r.stderr, err: r.err}
	if l.saysNoRepository() || l.saysGitFileNowhere() {
		return true
	}
	if !l.exit128Says("") {
		return false
	}
	text := strings.ToLower(r.stderr)
	for _, phrase := range []string{"detected dubious ownership", "cannot use bare repository", "unsafe repository"} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

// repoCheckFailedText is the error of a failed call about the repository in root.
// detail is the exec error or git's lines (allLines).
func repoCheckFailedText(root, detail string) string {
	return fmt.Sprintf("git could not check this repository on the remote machine: at %q: %s", root, detail)
}

// allLines is detail with every line that git wrote to its error output: the
// lines are joined by one space. The call about the repository has that form.
// For the two lines `fatal: line one of the wrapper for p3` and `hint: line two
// of the wrapper for p3` 5fd08069 answers `… at "<root>": fatal: line one of
// the wrapper for p3 hint: line two of the wrapper for p3` (cell N-f of
// val-s3p2b2-8-linux2, Linux VM, `git.info` and `git.status`). An empty line
// between two lines, and a line with spaces at its ends, are not measured:
// claustrum drops the empty line and those spaces.
func (r shadowRun) allLines() string {
	if lines := stderrLines([]byte(r.stderr)); lines != "" {
		return lines
	}
	return r.detail()
}

// printedProbePath is the path of git in the "disagree" text: out, the answer
// as git printed it, when only the direction of its slashes differs from found,
// the cleaned path. Every other answer is shown as found. sep is the separator
// of the system.
func printedProbePath(out, found, sep string) string {
	if strings.ReplaceAll(out, "/", sep) == found {
		return out
	}
	return found
}

// versionAfterFailure runs one `git version` call, with no option and no added
// variable. Its working directory is gitVersionDir. cannotRunText reads its answer, and
// the other callers do not. Among the measured places of that call on 5fd08069
// are these two:
//
//   - after a failed call about the repository that is not a "no repository"
//     answer. The requests have 13, 2, 2, 2 and 4 calls there. The build
//     before had 12, 1, 1, 1 and 2 (cell 39 of val-s3p2b2-5-linux2, Linux VM).
//     An exit 1 with a text has 13 and 2 calls (cells p3 of
//     val-s3p2b2-7-linux2, Linux VM). Three answers have no such call: "no
//     repository", an answer with another path, and an ended call (cell 39,
//     and cells 37 and 38).
//   - after a read of the config that failed with a syntax error, in each
//     request (cell x9-syntax-own of that round, the A5 cells of
//     val-s3p2b2-5-linux and val-s3p2b2-5-macos, and PA5 of
//     val-s3p2b2-5-windows).
//
// The call has the bound of a call of the start test. Its environment is
// gitVersionEnv(nil), also in the first `git version` call of shadowVersionNote
// (claustrum's own, not measured).
func versionAfterFailure() shadowRun {
	return boundedGit(shadowCallBound, gitVersionDir, gitVersionEnv(nil), "version")
}

// cannotRunText runs versionAfterFailure and returns the text of a git that
// cannot run, or "" when that call works. The text is gitCannotRunPrefix and
// the detail of the failed call, as in failedListingText. After a failed call 7
// of the start test, 5fd08069 answers `git cannot run on this host; git not
// run: exit status 128: fatal: bad config line 3 in file <home>/.gitconfig`
// (cells E1 to E5 of round task083-01 on a Linux VM, E1 and E2 on macOS and
// Windows VMs). askGit uses it too, and so does a create
// after a failed first checkout listing (hardenedGitCheckout). A call that
// exited 0 while a child kept its output pipes past shadowCallDrain works, as a
// test call does (exitedInTime).
func cannotRunText() string {
	v := exitedInTime(versionAfterFailure())
	if v.err == nil {
		return ""
	}
	return gitCannotRunPrefix + listingRun{stderr: v.stderr, err: v.err}.detail()
}

// keepProbe keeps a good answer for root, whose call ended now. Answers that
// are older than probeKeep go, so the map holds the roots of the last seconds.
func (h *shadowHost) keepProbe(root string) {
	h.probeMu.Lock()
	defer h.probeMu.Unlock()
	now := probeClock()
	if h.probes == nil {
		h.probes = map[string]keptProbe{}
	}
	for name, k := range h.probes {
		if now.Sub(k.end) >= probeKeep {
			delete(h.probes, name)
		}
	}
	h.probes[root] = keptProbe{end: now, root: probeStat(root), git: probeGitStat(filepath.Join(root, ".git"))}
}

// sameProbeFolder reports whether a and b are one folder on disk.
func sameProbeFolder(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	return err == nil && os.SameFile(fa, fb)
}

// asksGit reports whether a request without the member on the trust verdict t
// makes the call: the state is shadow and the trust check pinned a git
// directory. With a GIT_DIR or a GIT_COMMON_DIR in the daemon's own environment
// no call of the request names a copy (gitUse.names), and the call is not
// measured there: claustrum makes none.
func (h *shadowHost) asksGit(t gitDirTrust, off bool) bool {
	return !off && t.verdict == gitDirTrusted && t.pinCommonDir != "" && h.control(false) == controlShadow &&
		!daemonCommonDirSet() && os.Getenv("GIT_DIR") == ""
}

// recheck asks git again before a call of the request in dir, when the kept
// answer is no longer good. It runs before the listing of each hardened call
// that has no --git-dir= in its argv and names the copy (hookPrecursor).
//
// 5fd08069 tests the kept answer at more than one step of a request. A
// git.info that came 4.95 s after the call made the next call as its fifth,
// after `remote get-url origin` (cell 34 of s3p2b-edges-linux2, Linux VM). A
// create in a repository with no `.claude` folder made it as its third call,
// after `rev-parse --is-inside-work-tree` and before the next listing, where
// the daemon had made that folder (cell 36 of that round). A create whose
// `worktree add` moved the time of `.git` made no call after it: its later
// calls have --git-dir= (the same cell, and cell 26 of s3p2b-edges2-macos and
// of s3p2b-edges2-windows). The place before each such listing is fitted to
// those rows. A bad answer at such a step is not measured: the request goes on,
// and the next request asks again.
func (u gitUse) recheck(dir string) {
	if !u.shadow() || u.host == nil {
		return
	}
	t, ok := u.names(dir)
	if !ok || !u.host.asksGit(t, false) {
		return
	}
	u.host.askGit(t, dir)
}
