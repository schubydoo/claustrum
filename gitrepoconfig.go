package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// The daemon's own read of a repository's config, and its tests of three files of
// the git directory. 5fd08069 refuses a git method when it does not accept one of
// them. docs/PROTOCOL.md, "Repository config refusals", gives the rules, and
// docs/record/git-config-refusals.md the cells. A Linux VM with git 2.43.0 ran
// every cell that a comment names here, unless the comment names another system.
// The X cells are those of the edge round of that record.

const (
	// repoConfigPrefix starts a refusal of the config, and repoFilePrefix one of
	// another file of the git directory.
	repoConfigPrefix = "this repository's git config cannot be used: "
	repoFilePrefix   = "a file in this repository's .git directory cannot be used: "

	// repoConfigMaxIncludes is the most included files that the daemon reads. 16
	// in a row pass, and 17 are refused (cells E10, Linux and macOS VMs).
	repoConfigMaxIncludes = 16
	// repoConfigMaxDepth is the deepest include chain. config, d1, d2 and d3 pass,
	// and a fourth level is refused (cells E10, Linux and macOS VMs).
	repoConfigMaxDepth = 3
	// repoConfigMaxBytes bounds one config file. 8388608 bytes pass, and 8388609
	// are refused (cells X6).
	repoConfigMaxBytes = 8 << 20
	// repoGitFileMaxBytes bounds info/attributes, info/exclude and shallow. 4194304
	// bytes pass, and 4194305 are refused (cells X6).
	repoGitFileMaxBytes = 4 << 20
	// repoConfigKeyMaxBytes bounds the full name of a setting, with its section and
	// subsection, in the sections of repoNameBoundSections. 1024 bytes pass, and
	// 1025 are refused, in core and remote (cells X4).
	repoConfigKeyMaxBytes = 1024
	// repoSettingCost and repoSettingsMaxCost make the "more settings" bound. The
	// arithmetic is in docs/PROTOCOL.md, "The count of settings". It is fitted to
	// the measured pairs of cells X5. TestRepoConfigSettingsCost has each pair.
	repoSettingCost     = 96
	repoSettingsMaxCost = 8 << 20
)

// repoNameBoundSections are the sections whose setting names have the bound of
// repoConfigKeyMaxBytes. The exact pair is measured in core and remote (cells
// X4), and in lfs, url, filter, extensions, include and includeif (cells V4).
// Cells N6 measured the same pair in index, feature and checkout (Linux, macOS
// and Windows VMs). A name of 1100 or 2200 bytes passes in
// the sections user, alias, http, credential, safe, gc, diff, merge, x, abcde,
// branch and submodule, and so does a section name of 1100 bytes. A subsection
// of 1100 bytes passes in branch (cell V4). No other section was run.
var repoNameBoundSections = map[string]bool{
	"core": true, "extensions": true, "include": true, "includeif": true,
	"lfs": true, "url": true, "filter": true, "remote": true,
	"index": true, "feature": true, "checkout": true,
}

// repoCountedSections are the sections whose settings count toward
// repoSettingsMaxCost (cells X5 and V5). The exact pairs are in core and remote.
// For each other section here one cell of 200000 lines was refused. The sections
// x and user do not count. No other section was run: add a measured one here.
var repoCountedSections = map[string]bool{
	"core": true, "remote": true, "extensions": true, "include": true, "branch": true,
	"lfs": true, "index": true, "url": true, "filter": true, "feature": true,
	"checkout": true, "includeif": true,
}

// repoKnownExtensions are the extension names that pass under format version 1
// (cells E9, X8 and X9). claustrum refuses every other name, with a value or with
// none. The names that ran are `foo`, `bar`, `noop2` and `x.noop`, the name of
// `[extensions "x"] noop` (cells V6, Linux, macOS and Windows VMs).
var repoKnownExtensions = map[string]bool{
	"objectformat": true, "compatobjectformat": true, "refstorage": true,
	"worktreeconfig": true, "relativeworktrees": true, "submodulepathconfig": true,
	"noop-v1": true, "noop": true, "preciousobjects": true, "partialclone": true,
}

// repoRefusedExtensionValues are the measured values of a known extension that
// are refused (cells E9, X8 and V6). `partialclone` with no value or an empty
// value is refused too. Every other value of a known name passes: it is measured
// as accepted, or it is not measured.
var repoRefusedExtensionValues = map[string]string{
	"objectformat":        "md5",
	"refstorage":          "banana",
	"preciousobjects":     "maybe",
	"relativeworktrees":   "maybe",
	"submodulepathconfig": "maybe",
	"noop-v1":             "maybe",
	"compatobjectformat":  "md5",
}

// startsWorktreeConfigTest reports whether value, as the value of
// extensions.worktreeconfig, starts the test of a `config.worktree` file: a true
// word of git in any letter case. The key with no value starts it too. `true`,
// `yes`, `1` and `TRUE` are measured in cells X2 and V14, and `on` and `ON` in
// cells N4 (Linux, macOS and Windows VMs). `false`, `no` and `0` start no test
// (cells N4). `Yes`, `On` and `tRuE` start it (cells P2, the same three systems).
func startsWorktreeConfigTest(value string) bool {
	switch strings.ToLower(value) {
	case "true", "yes", "on", "1":
		return true
	}
	return false
}

// listConfigText runs `git config --no-includes --file - --list -z` with text as
// its standard input, and returns the listing. git follows no include in this
// call. dir and env are the working directory and the environment of the call:
// "" and nil keep those of the daemon.
func listConfigText(ctx context.Context, text []byte, dir string, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", "config", "--no-includes", "--file", "-", "--list", "-z")
	cmd.Stdin = bytes.NewReader(text)
	cmd.Dir = dir
	cmd.Env = env
	return cmd.Output()
}

// repoListingMaxBytes bounds the listing that the read of the repository config
// takes from git for one file. A listing names the whole key of each setting, so
// a small file with a long subsection name and many keys lists as far more bytes
// than it has. By arithmetic, the largest accepted files of cells X5 and X6 list
// as no more than their own size. No cell measured a listing size. The bound is
// claustrum's own, and 5fd08069 is not measured with a listing over it. A
// variable, so that a test can lower it.
var repoListingMaxBytes = 8 * repoConfigMaxBytes

// errListingTooLarge ends a listing that passes repoListingMaxBytes.
var errListingTooLarge = errors.New("the listing of a config file is over its bound")

// boundedListing is the standard output of one listing of the read. It keeps the
// first max bytes. A write that passes max fails, and os/exec then ends its copy
// from git.
type boundedListing struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (b *boundedListing) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); len(p) > room {
		b.over = true
		n, _ := b.buf.Write(p[:room])
		return n, errListingTooLarge
	}
	return b.buf.Write(p)
}

// listRepoConfigText is listConfigText for the read of the repository config:
// the listing is bound by repoListingMaxBytes before it is in memory, and the
// error output of git is returned beside it. A listing over the bound returns
// errListingTooLarge and the whole settings of its first repoListingMaxBytes
// bytes.
func listRepoConfigText(ctx context.Context, text []byte, dir string, env []string) (out, stderr []byte, err error) {
	cmd := exec.CommandContext(ctx, "git", "config", "--no-includes", "--file", "-", "--list", "-z")
	cmd.Stdin = bytes.NewReader(text)
	cmd.Dir = dir
	cmd.Env = env
	listing := &boundedListing{max: repoListingMaxBytes}
	errText := &cappedText{max: 1 << 16}
	cmd.Stdout = listing
	cmd.Stderr = errText
	err = cmd.Run()
	out = listing.buf.Bytes()
	if listing.over {
		return out[:bytes.LastIndexByte(out, 0)+1], nil, errListingTooLarge
	}
	return out, errText.buf, err
}

// repoConfigListEnv is the environment of the listing of one config file: that of
// the daemon, then GIT_DIR=<null device>, LC_ALL=C and LANGUAGE=C. The call log of
// 5fd08069 shows these three values on a Linux VM, and GIT_DIR=NUL on a Windows VM
// (cells C2a). The daemon's GIT_CONFIG and GIT_CONFIG_PARAMETERS go, as for every
// listing of claustrum (not measured here).
func repoConfigListEnv() []string {
	return append(listingEnvBase(), "GIT_DIR="+os.DevNull, "LC_ALL=C", "LANGUAGE=C")
}

// stdinSyntaxError matches the text of git for a syntax error in a file that it
// reads on standard input.
var stdinSyntaxError = regexp.MustCompile(`(?m)^fatal: bad config line ([0-9]+) in standard input\s*$`)

// openRepoFile opens path for a read. The open does not wait, so a FIFO with no
// writer is judged at once: 5fd08069 answers in under 350 ms there (cells C4, C5
// and E10). A file that is missing, a dangling link included, gives nil and no
// error (cells C3 and X10).
//
// The open never makes the file the controlling terminal of the daemon
// (openNoTerminal). With a config that is a link to a pty slave, 89cb6289 and
// 5fd08069 stay up when the pty master closes (cell V17, Linux VM, 6 of 6 runs).
func openRepoFile(path string) (*os.File, error) {
	f, err := repoFileOpen(path, os.O_RDONLY|openNonBlocking|openNoTerminal, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return f, err
}

// repoFileOpen is the open of openRepoFile. A test replaces it.
var repoFileOpen = os.OpenFile

// repoFileKindDetail tests the open file f of path: it must be a regular file of
// at most max bytes. A symbolic link to a regular file passes, and one to the
// null device does not (cells C4 and C5). large is the end of the size refusal.
// The path is quoted and cut as quoteOperand does it: the first 300 characters
// stay, with no mark of the cut. Cells X12 measured that cut for ASCII paths in
// the size text of the config, and cells V12 with characters of two bytes.
func repoFileKindDetail(f *os.File, path string, max int64, large string) string {
	fi, err := f.Stat()
	switch {
	case err != nil:
		// Not measured. claustrum leaves the file to git.
		return ""
	case !fi.Mode().IsRegular():
		return quoteOperand(path) + notRegularTail
	case fi.Size() > max:
		return quoteOperand(path) + large
	}
	return ""
}

// readRepoFile reads the open regular file f of path. A file that grew past max
// after the size test gets the size refusal (not measured). ok is false, with no
// detail, when the read failed.
func readRepoFile(f *os.File, path string, max int64, large string) (b []byte, detail string, ok bool) {
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, "", false
	}
	if int64(len(b)) > max {
		return nil, quoteOperand(path) + large, false
	}
	return b, "", true
}

// configLarge is the end of the size refusal of a config file.
const configLarge = " is larger than 8 MiB"

// notRegularTail is the end of the refusal of a file that is not a regular file.
const notRegularTail = " is not a regular file"

// repoExtension is one `extensions.<name>` setting, in the order of the read.
type repoExtension struct {
	name, value string
	hasValue    bool
}

// refused reports whether the extension is refused under format version 1.
func (e repoExtension) refused() bool {
	if !repoKnownExtensions[e.name] {
		return true
	}
	if e.name == "partialclone" && e.value == "" {
		return true
	}
	v, listed := repoRefusedExtensionValues[e.name]
	return listed && e.hasValue && e.value == v
}

// repoFileState is the state of one file at the time of a read: what Lstat and
// Stat gave for its path. A nil value is a path that the call did not answer for,
// such as a missing file.
type repoFileState struct {
	path         string
	link, target os.FileInfo
}

func readRepoFileState(path string) repoFileState {
	s := repoFileState{path: path}
	if fi, err := os.Lstat(path); err == nil {
		s.link = fi
	}
	if fi, err := os.Stat(path); err == nil {
		s.target = fi
	}
	return s
}

// sameFileState reports whether a and b show one file in one state: the same
// identity as os.SameFile sees it, size, modification time and mode. On Linux
// and macOS the identity is the device and the inode. What os.SameFile compares
// for a kept state on Windows is not tested here. Two missing files are equal.
func sameFileState(a, b os.FileInfo) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && a.Mode() == b.Mode()
}

// unchanged reports whether the file is still in the recorded state.
func (s repoFileState) unchanged() bool {
	now := readRepoFileState(s.path)
	return sameFileState(s.link, now.link) && sameFileState(s.target, now.target)
}

// repoReadLog holds what one read of repoConfigGate used: the state of each file
// that a test named, taken before that test, and whether the read has a verdict.
// failed is true when an open or a read of the daemon ended with an error. The
// verdict of such a read answers its request and is not kept: the error can be
// one of the daemon itself, such as too many open files. A nil log records
// nothing.
type repoReadLog struct {
	states    []repoFileState
	seen      map[string]bool
	noVerdict bool
	failed    bool
}

// fail notes an error of an open or a read.
func (l *repoReadLog) fail() {
	if l != nil {
		l.failed = true
	}
}

// note records the state of path, once.
func (l *repoReadLog) note(path string) {
	if l == nil || l.seen[path] {
		return
	}
	if l.seen == nil {
		l.seen = map[string]bool{}
	}
	l.seen[path] = true
	l.states = append(l.states, readRepoFileState(path))
}

// repoConfigRead is one read of a repository's config with its included files.
type repoConfigRead struct {
	ctx context.Context
	// log gets each file of the read, or is nil.
	log *repoReadLog
	// gitDir is the git directory that holds the config.
	gitDir string
	// includes counts the included files that were read.
	includes int
	// version is the last value of core.repositoryformatversion in the config
	// itself, when hasVersion.
	version    string
	hasVersion bool
	// extensions are those of the config itself.
	extensions []repoExtension
	// worktreeConfig is true when the last extensions.worktreeconfig setting of
	// the config itself has no value or one that startsWorktreeConfigTest accepts.
	worktreeConfig bool
	// includedCost is the sum of the counted settings of the included files.
	includedCost int
	// unlisted is true when git did not list a file, or listed more than
	// repoListingMaxBytes, and the read has no text for that. The read then gives
	// no verdict, and the listing of the method answers as before.
	unlisted bool
	// settings are the settings of the config and of its included files, each
	// included file at the place of its include line. worktree are those of the
	// `config.worktree` file of the git directory. The copy of the settings is
	// written from both (shadowConfigText).
	settings []repoSetting
	worktree []repoSetting
}

// file reads the config file at path, depth levels below the config, and returns
// the detail of a refusal, or "". Each file is one git call (cells E10: 17 such
// calls for the config and 16 included files). An `include.path` setting is
// followed where it stands. An empty value, and a setting with no value, name no
// file (cells X9).
//
// Three tests are for the config itself, depth 0, and not for an included file:
// the format version, the extensions and the worktreeConfig switch. A version
// `2` and an `extensions.foo` in an included file are not refused (cells X7 and
// X9). The name bound and the "more settings" bound hold in an included file
// too, with the path of that file (cells V7, Linux, macOS and Windows VMs). The
// count is one sum across the included files, and the config has its own sum.
// Two included files of 40000 `core.filemode = true` lines each are refused,
// and the text names the second file. The config with 40000 such lines and one
// such included file passes (cells N5, Linux, macOS and Windows VMs). A Linux VM
// ran that pair in both orders. A file below an included file adds to the same
// sum: a chain of three files of 30000 such lines is refused, and the text names
// the third file. A chain of two passes (cells P3, the same three systems).
//
// An open that fails is a refusal with the error text of the open and no quotes:
// a socket and a file of mode 000, as the config (cells X10) and as an included
// file (cells V7, Linux VM, and mode 000 on a macOS VM). A syntax error has the
// text of git with the path in quotes, in the config (cell X10) and in an
// included file (cells V7, Linux, macOS and Windows VMs).
//
// Both bounds are tested on the whole file before an include of it is followed.
// That order is fitted to two cells: 200000 `include.path` lines of a missing
// file (cells X5), and 200000 such lines in an `includeIf` section (cell V5,
// Linux VM). Both get the "more settings" text, not the include limit.
//
// Each `include.path` setting with a value counts toward the 16 files, also when
// its file is missing: 17 missing files are refused (cells V8, Linux, macOS and
// Windows VMs), and 16 pass (cells V8, Linux VM, and P8w, Windows VM). The limits
// come before the open of the file (not measured apart from the missing file).
//
// The file of an `includeIf` section counts toward the 16, and it gets the kind
// test and the size test, whatever the condition is: `gitdir:` that matches or
// not, `gitdir/i:`, `onbranch:` and `hasconfig:` (cells X9 and V8, Linux, macOS
// and Windows VMs). The value tests of includeTarget run too: `~root/x` gets the
// "expands neither" text in a matching section (cell V8) and in one that does
// not match (cells N12, Linux VM). A missing file of such a section counts
// toward the 16 (cells N7, Linux, macOS and Windows VMs). 5fd08069 lists such a
// file with one more git call, with or without a match, and carries no setting
// of it (cells 11a to 11f of s3p2b-edges-linux, Linux VM, and cell 11 of that
// round on a macOS VM). claustrum makes that call and does not use its answer:
// a file there that git cannot list is not measured.
func (r *repoConfigRead) file(path string, depth int) string {
	r.log.note(path)
	if d := configLinkDetail(path); d != "" {
		return d
	}
	if depth > 0 {
		if d := r.countInclude(depth); d != "" {
			return d
		}
	}
	f, err := openRepoFile(path)
	if err != nil {
		r.log.fail()
		return err.Error()
	}
	if f == nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	if d := repoFileKindDetail(f, path, repoConfigMaxBytes, configLarge); d != "" {
		return d
	}
	text, detail, ok := readRepoFile(f, path, repoConfigMaxBytes, configLarge)
	if !ok {
		r.unlisted = detail == ""
		return detail
	}
	out, errOut, err := listRepoConfigText(r.ctx, text, gitVersionDir, repoConfigListEnv())
	// A listing over its bound still gets the name bound and the count of the
	// settings that the read kept. Those two refusals are the first in the order
	// of the listing, so they are the ones of the whole listing. With neither,
	// the read has no verdict.
	over := errors.Is(err, errListingTooLarge)
	if err != nil && !over {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if m := stdinSyntaxError.FindSubmatch(errOut); m != nil {
				// 5fd08069 runs one `git version` after this read
				// (versionAfterFailure). An included file with a syntax error is
				// not measured: it gets the call too.
				versionAfterFailure()
				return "fatal: bad config line " + string(m[1]) + " in " + quoteOperand(path)
			}
		}
		r.unlisted = true
		return ""
	}
	cost := &r.includedCost
	if depth == 0 {
		cost = new(int)
	}
	for rec := range bytes.SplitSeq(out, []byte{0}) {
		key, value, hasValue := strings.Cut(string(rec), "\n")
		section, _, _ := strings.Cut(key, ".")
		if len(key) > repoConfigKeyMaxBytes && repoNameBoundSections[section] {
			return quoteOperand(path) + " has a setting in section " + strconv.Quote(section) +
				" whose name is longer than " + strconv.Itoa(repoConfigKeyMaxBytes) + " bytes"
		}
		if repoCountedSections[section] {
			if *cost += len(key) + len(value) + repoSettingCost; *cost > repoSettingsMaxCost {
				return quoteOperand(path) + " has more settings than Claude reads (8 MiB); remove repeated ones"
			}
		}
		if depth > 0 {
			continue
		}
		switch key {
		case "core.repositoryformatversion":
			r.version, r.hasVersion = value, true
		case "extensions.worktreeconfig":
			r.worktreeConfig = !hasValue || startsWorktreeConfigTest(value)
		}
		if name, ok := strings.CutPrefix(key, "extensions."); ok {
			r.extensions = append(r.extensions, repoExtension{name: name, value: value, hasValue: hasValue})
		}
	}
	if over {
		r.unlisted = true
		return ""
	}
	for rec := range bytes.SplitSeq(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		key, value, hasValue := strings.Cut(string(rec), "\n")
		r.settings = append(r.settings, repoSetting{key: key, value: value, hasValue: hasValue, included: depth > 0})
		if value == "" {
			continue
		}
		switch {
		case key == "include.path":
			target, detail := includeTarget(path, value, r.gitDir)
			if detail != "" {
				return detail
			}
			if d := r.file(target, depth+1); d != "" || r.unlisted {
				return d
			}
		case isIncludeIfPath(key):
			target, detail := includeTarget(path, value, r.gitDir)
			if detail != "" {
				return detail
			}
			if d := r.countInclude(depth + 1); d != "" {
				return d
			}
			if d := configFileTests(target, r.log); d != "" {
				return d
			}
			listConfigFile(r.ctx, target)
		}
	}
	return ""
}

// listConfigFile lists the config file at path with one git call, as the read
// lists the config. ok is false, with no call, for a file that is missing, that
// is not a regular file or that is over repoConfigMaxBytes. It is false too
// when git did not list the file. The open does not wait (openRepoFile).
func listConfigFile(ctx context.Context, path string) (settings []repoSetting, ok bool) {
	_, settings, ok = listConfigFileText(ctx, path)
	return settings, ok
}

// listedSettings are the settings of out, the output of one `config --list -z`
// call, in its order.
func listedSettings(out []byte) (settings []repoSetting) {
	for rec := range bytes.SplitSeq(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		key, value, hasValue := strings.Cut(string(rec), "\n")
		settings = append(settings, repoSetting{key: key, value: value, hasValue: hasValue})
	}
	return settings
}

// countInclude counts one more included file, depth levels below the config. It
// returns the refusal of a file past the 16 or below the third level, or "".
func (r *repoConfigRead) countInclude(depth int) string {
	if depth > repoConfigMaxDepth || r.includes == repoConfigMaxIncludes {
		return "its includes go past what Claude follows (" + strconv.Itoa(repoConfigMaxIncludes) +
			" files, nested " + strconv.Itoa(repoConfigMaxDepth) + " deep)"
	}
	r.includes++
	return ""
}

// isIncludeIfPath reports whether key is the path setting of an `includeIf`
// section with a condition, as git lists it: `includeif.<condition>.path`.
func isIncludeIfPath(key string) bool {
	cond, ok := strings.CutPrefix(key, "includeif.")
	if !ok {
		return false
	}
	_, ok = strings.CutSuffix(cond, ".path")
	return ok
}

// configFileTests are the kind test and the size test of a config file that the
// daemon does not read: the file of an `includeIf` section, and a
// `config.worktree` file. A FIFO there gets the "not a regular file" text, and a
// file of 20 MiB the size text, each with the path of that file (cells X9, V8
// and X2). A version `2` in such a file is not refused, and no other content of
// it ran. An open error there is not measured: claustrum gives no refusal.
func configFileTests(path string, log *repoReadLog) string {
	log.note(path)
	if d := configLinkDetail(path); d != "" {
		return d
	}
	f, err := openRepoFile(path)
	if err != nil {
		log.fail()
	}
	if f == nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	return repoFileKindDetail(f, path, repoConfigMaxBytes, configLarge)
}

// includeTarget is the path of the file that the `include.path` value of file
// names, or the detail of a refusal. A relative value counts from the folder of
// file, also when it leaves the git directory (cell X9), and `~/` is the home
// folder of the daemon (cells C3). `~user` and `%(prefix)/` are refused (cells
// C3, `~root/x` and `%(prefix)/x`). A `~/` value with no home folder gets the
// same refusal: the text says so, and no daemon starts with no home folder, so
// no cell ran it. The Windows rules come first (includeValueDetail and
// includePlaceDetail).
//
// The value in the text is cut to its first 100 characters: a value of 100 is
// whole, and one of 101 is cut, also with characters of two bytes (cells V12,
// Linux and macOS VMs).
//
// The path of a relative include is the folder of file, one separator, and the
// value as written. It is not cleaned: on a Windows VM `path = <dir>/inc.cfg`
// gives `<git dir>\<dir>/inc.cfg` in a text (cells V12c and V12d). No cell on
// Linux or macOS has a value that a clean changes. The folder of file stays as
// written too: `include.path = a/../b/inc.cfg`, and in that file `path = x.cfg`,
// gives `<git dir>/a/../b/x.cfg` in a text (cell N13-b, Linux and macOS VMs). The `~/` form
// is joined and cleaned as before (not measured). `include.path = ~` names the
// home folder itself, and gets the "not a regular file" text (cell N13, Linux
// VM).
func includeTarget(file, value, gitDir string) (target, detail string) {
	if d := includeValueDetail(file, value); d != "" {
		return "", d
	}
	unexpanded := quoteOperand(file) + " includes " + quoteCut(value, includeValueMaxRunes) +
		", and Claude expands neither ~user nor %(prefix), nor ~ where no home directory is set; write that path out in full"
	switch {
	case value == "~" || strings.HasPrefix(value, "~/"):
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", unexpanded
		}
		target = filepath.Join(home, value[1:])
	case strings.HasPrefix(value, "~") || strings.HasPrefix(value, "%(prefix)/"):
		return "", unexpanded
	case filepath.IsAbs(value):
		target = value
	default:
		target = folderAsWritten(file) + string(filepath.Separator) + value
	}
	return target, includePlaceDetail(target, gitDir)
}

// folderAsWritten is the folder part of the path file: the text before its last
// separator. It is not cleaned, as filepath.Dir cleans it.
func folderAsWritten(file string) string {
	for i := len(file) - 1; i >= 0; i-- {
		if os.IsPathSeparator(file[i]) {
			return file[:i]
		}
	}
	return "."
}

// includeValueMaxRunes is the cut of an include value in the "expands neither"
// text. versionValueMaxRunes and extensionNameMaxRunes are the cuts of the
// format version value and of the extension name in their texts.
const (
	includeValueMaxRunes  = 100
	versionValueMaxRunes  = 20
	extensionNameMaxRunes = 60
)

// repoConfigDetail reads the config of the git directory gitDir and returns the
// detail of a class 1 refusal, or "". entry is the git directory of a linked
// worktree that the request names, or "".
//
// The format version is the last value of core.repositoryformatversion in the
// config itself, as git lists it. It must be exactly the text 0 or 1 (cells C2,
// E8 and X7). A config with no such setting is not refused (cell X7). Under
// version 1, the first extension of the config itself that is refused gives its
// name (repoExtension.refused). Under version 0 no extension is refused (cells
// C2, A3 and X8). The version test comes before the extension test (not
// measured). An extension in a config with no version key is not refused (cells
// V6, `foo = true`, Linux, macOS and Windows VMs).
//
// With `extensions.worktreeConfig` in the config, as a true word of git
// (startsWorktreeConfigTest) or with no value, the `config.worktree` file of entry
// gets the kind test and the size test. Cells X2 ran git.info with a linked
// worktree as its path. Cells V14 ran git.status, git.list_branches and
// git.worktree_create too (Linux and Windows VMs), and a remove (cell W14, Linux
// VM). The `config.worktree` file of the main worktree is not measured for these
// two tests, and claustrum does not test it.
//
// The version value in its text is cut to its first 20 characters, with no mark
// of the cut. The extension name is cut to its first 60. A value of 20
// characters is whole, and one of 21 shows 20. A name of 60 is whole, and one of
// 61 shows 60 (cells P1, Linux, macOS and Windows VMs). The cells have ASCII
// text. claustrum counts characters, as for a path.
func repoConfigDetail(ctx context.Context, gitDir, entry string, log *repoReadLog) string {
	r, d := readRepoConfig(ctx, gitDir, log)
	if d != "" || r.unlisted {
		return d
	}
	return entryConfigDetail(r.worktreeConfig, entry, log)
}

// entryConfigDetail tests the `config.worktree` file of entry, the git
// directory of a linked worktree, when the repository has the worktreeConfig
// switch. entry is "" for every other git directory.
func entryConfigDetail(worktreeConfig bool, entry string, log *repoReadLog) string {
	if worktreeConfig && entry != "" {
		return configFileTests(filepath.Join(entry, "config.worktree"), log)
	}
	return ""
}

// readRepoConfig is the read of the config of gitDir with its included files.
// It returns the read and the detail of a refusal, or "".
//
// With the worktreeConfig switch and a `config.worktree` file in gitDir,
// 5fd08069 lists that file two times, right after the config and its included
// files. It does not list it when the switch is off or the file is missing
// (cells 1a to 1e, 9a and 9k of s3p2b-edges2-linux, Linux VM). claustrum lists
// it two times and takes the settings of the first listing. A file there that
// is not a regular file, that is over the size bound of a config or that git
// cannot list is not measured: its settings are not used and nothing is
// refused.
func readRepoConfig(ctx context.Context, gitDir string, log *repoReadLog) (*repoConfigRead, string) {
	r := &repoConfigRead{ctx: ctx, gitDir: gitDir, log: log}
	if d := r.file(filepath.Join(gitDir, "config"), 0); d != "" || r.unlisted {
		if log != nil {
			log.noVerdict = r.unlisted
		}
		return r, d
	}
	if r.hasVersion && r.version != "0" && r.version != "1" {
		return r, "the repository uses format version " + quoteCut(r.version, versionValueMaxRunes) +
			", which this version of Claude does not support"
	}
	if r.version == "1" {
		for _, e := range r.extensions {
			if e.refused() {
				return r, "the repository uses git extension " + quoteCut(e.name, extensionNameMaxRunes) +
					", or a value of it, that this version of Claude does not support"
			}
		}
	}
	if r.worktreeConfig {
		path := filepath.Join(gitDir, "config.worktree")
		log.note(path)
		if settings, ok := listConfigFile(ctx, path); ok {
			r.worktree = settings
			listConfigFile(ctx, path)
		}
	}
	return r, ""
}

// gitDirFilesDetail tests shallow, info/attributes and info/exclude of the git
// directory gitDir, in that order, and returns the detail of a class 2 refusal,
// or "". With two refused files 5fd08069 names `shallow` before info/attributes
// (cells V16, Linux, macOS and Windows VMs) and info/attributes before
// info/exclude. Cells P6 ran that pair four ways on a Linux VM: a file of 20
// MiB with a FIFO, two files of 20 MiB, two FIFOs, and a FIFO with a directory.
// A refused config comes before each of them (cells V16).
func gitDirFilesDetail(gitDir string, log *repoReadLog) string {
	path := filepath.Join(gitDir, "shallow")
	log.note(path)
	if d := shallowFileDetail(path, log); d != "" {
		return d
	}
	for _, name := range []string{"attributes", "exclude"} {
		path := filepath.Join(gitDir, "info", name)
		log.note(path)
		if d := infoFileDetail(path); d != "" {
			return d
		}
	}
	return ""
}

// gitDirFilesRefusal is gitDirFilesDetail with the prefix of its refusal, or "".
func gitDirFilesRefusal(gitDir string) string {
	if d := gitDirFilesDetail(gitDir, nil); d != "" {
		return repoFilePrefix + d
	}
	return ""
}

// infoFileDetail tests info/attributes or info/exclude at path. The daemon does
// not open the file. A FIFO and a directory are refused as not regular (cells C5
// and X10), and a regular file of more than repoGitFileMaxBytes too (cells E6 and
// X6). A symbolic link passes, also one to the null device and a dangling one,
// and so does a file of mode 000 (cells C5 and X10). A link to a FIFO or to a
// large file is not measured: it passes here, as every link does.
func infoFileDetail(path string) string {
	fi, err := os.Lstat(path)
	switch {
	case err != nil || fi.Mode()&fs.ModeSymlink != 0:
		return ""
	case !fi.Mode().IsRegular():
		return quoteOperand(path) + notRegularTail
	case fi.Size() > repoGitFileMaxBytes:
		return quoteOperand(path) + ": it is larger than 4 MiB"
	}
	return ""
}

// shallowFileDetail tests the `shallow` file at path. The daemon opens it and
// follows a link: a FIFO, a directory and a link to the null device are refused
// as not regular (cells C5 and X10). A file of mode 000 gets the error text of
// the open, with no quotes (cell X10), and so does a socket (cell V10). Another
// open error gets the same form (not measured). A dangling link and a missing
// file pass.
// The size text has no `: it` (cell X6). The file holds object ids and nothing
// else (holdsObjectIDs).
func shallowFileDetail(path string, log *repoReadLog) string {
	const large = " is larger than 4 MiB"
	f, err := openRepoFile(path)
	if err != nil {
		log.fail()
		return err.Error()
	}
	if f == nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	if d := repoFileKindDetail(f, path, repoGitFileMaxBytes, large); d != "" {
		return d
	}
	b, detail, ok := readRepoFile(f, path, repoGitFileMaxBytes, large)
	if !ok {
		if detail == "" {
			log.fail()
		}
		return detail
	}
	if !holdsObjectIDs(b) {
		return quoteOperand(path) + " holds something other than object ids"
	}
	return ""
}

// holdsObjectIDs reports whether each line of b is an object id: 40 or 64 hex
// characters in lower case. An empty file passes, and so do a last line with no
// newline and lines that end with CRLF. An id in upper case, and a blank line
// before or after an id, are refused (cells E7, X10 and X11). So are a line of
// 39, 41 or 63 hex characters, an id with a space after it, and a file that holds
// one line end and nothing else, LF or CRLF (cells V10, Linux, macOS and Windows
// VMs). This rule is fitted to those cells.
func holdsObjectIDs(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	for line := range strings.SplitSeq(strings.TrimSuffix(string(b), "\n"), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if (len(line) != 40 && len(line) != 64) || strings.Trim(line, "0123456789abcdef") != "" {
			return false
		}
	}
	return true
}

// repoVerdictKey names one read: the pinned git directory and the home folder of
// the daemon, which a `~/` include uses. A request on a linked worktree uses the
// read of its repository: on 5fd08069 such a request lists the config of the
// repository no second time (cell 1c of s3p2b-edges2-linux, Linux VM).
type repoVerdictKey struct {
	gitDir, home string
}

// repoVerdict is the outcome of one read, with the state of each file that the
// read used. Those two are not changed after the read.
type repoVerdict struct {
	// refusal is the refusal of the config, with its prefix, or "". The test of
	// the `config.worktree` file of a linked worktree and the tests of the other
	// files of the git directory come after it, in each request
	// (repoConfigGate).
	refusal string
	// worktreeConfig is true when the config has the worktreeConfig switch.
	worktreeConfig bool
	// shadowText is the text of the copy of the settings (shadowConfigText).
	shadowText string
	// worktreeItems are the `-c` items of the `config.worktree` file of the git
	// directory (worktreeConfigItems), or nil.
	worktreeItems []string
	// sharedGroup is true when the last `core.sharedRepository` setting of the
	// config and its included files is the word `group` (makeWorktreesFolder).
	// Every other value is not measured.
	sharedGroup bool
	states      []repoFileState
}

// repoVerdictsMax is the most git directories and linked worktrees whose verdict
// the daemon keeps. The oldest one goes first. The number is claustrum's own.
const repoVerdictsMax = 256

// repoVerdicts keeps the last verdict of each repository. order has the keys,
// the oldest first.
var repoVerdicts struct {
	sync.Mutex
	byKey map[repoVerdictKey]*repoVerdict
	order []repoVerdictKey
}

// keptRepoVerdict is the kept verdict of k, if every file of its read is in the
// recorded state. A file that was missing must still be missing.
func keptRepoVerdict(k repoVerdictKey) (*repoVerdict, bool) {
	repoVerdicts.Lock()
	v, ok := repoVerdicts.byKey[k]
	repoVerdicts.Unlock()
	if !ok {
		return nil, false
	}
	for _, s := range v.states {
		if !s.unchanged() {
			return nil, false
		}
	}
	return v, true
}

func keepRepoVerdict(k repoVerdictKey, v *repoVerdict) {
	repoVerdicts.Lock()
	defer repoVerdicts.Unlock()
	if repoVerdicts.byKey == nil {
		repoVerdicts.byKey = map[repoVerdictKey]*repoVerdict{}
	}
	if _, ok := repoVerdicts.byKey[k]; !ok {
		if len(repoVerdicts.order) >= repoVerdictsMax {
			delete(repoVerdicts.byKey, repoVerdicts.order[0])
			repoVerdicts.order = slices.Delete(repoVerdicts.order, 0, 1)
		}
		repoVerdicts.order = append(repoVerdicts.order, k)
	}
	repoVerdicts.byKey[k] = v
}

// repoReadVerdict is the verdict for the repository of the trust verdict t, or
// nil for a read with no verdict. It reads the config of the git directory that
// the trust check pinned, so a linked worktree gets the config of its repository
// (cells X2).
//
// On 5fd08069 a second git.info on a repository with an accepted config starts
// no git call for the read (cells B4 and V19). A request after a change of the
// config starts one (cell B4). With a refused format version each later request
// of that build makes the read again (cells N10 and P5, Linux, macOS and Windows
// VMs). A kept read with a refusal is not used: claustrum reads again, too.
// claustrum keeps the verdict of a read with the state of each file that
// the read used. It reads again when one of them has another identity, size,
// modification time or mode. It reads again when a file that was missing is
// there. That rule is claustrum's own: cell
// B4 ran one repeat and one changed config. A change that keeps all four values
// is not seen. A read with no verdict is not kept. A read in which an open or a
// read ended with an error is not kept either (repoReadLog.failed): its verdict
// answers this request, and the next request reads again. So a refusal with an
// `open <file>:` text is read again on each request. That rule is claustrum's
// own.
func repoReadVerdict(t gitDirTrust) *repoVerdict {
	home, _ := os.UserHomeDir()
	k := repoVerdictKey{gitDir: t.pinCommonDir, home: home}
	if v, ok := keptRepoVerdict(k); ok && v.refusal == "" {
		return v
	}
	ctx, cancel := gitCtx()
	defer cancel()
	var log repoReadLog
	v := &repoVerdict{}
	r, d := readRepoConfig(ctx, t.pinCommonDir, &log)
	switch {
	case d != "":
		v.refusal = repoConfigPrefix + d
	case log.noVerdict:
		return nil
	default:
		v.worktreeConfig = r.worktreeConfig
		v.shadowText = shadowConfigText(r.settings, r.worktree)
		v.worktreeItems = worktreeConfigItems(r.worktree)
		for _, s := range r.settings {
			if s.key == "core.sharedrepository" {
				v.sharedGroup = s.hasValue && s.value == "group"
			}
		}
	}
	v.states = log.states
	if !log.failed {
		keepRepoVerdict(k, v)
	}
	return v
}

// repoConfigGate is the refusal text of a git method for the repository of the
// trust verdict t, or "". It runs before the first git call of the method in
// the repository. accepted is the read of a repository that got no refusal of
// any kind, or nil.
//
// off is the `shadowDirOff` member of the request. With the JSON value true,
// 5fd08069 gives no such refusal for that request. Format version 2 gets the
// "not a repository" answers, and the files of 20 MiB get normal answers. A FIFO
// as the config gets no answer in 10 s, as on 89cb6289. Cells X1 measured that on
// git.info and git.status, and cells V2 on the three other methods (Linux and
// macOS VMs, and Windows with no FIFO). So nothing is read then.
//
// It runs for a trusted git directory only. A folder whose HEAD is no ref keeps
// its "not a repository" answers, also with format version 2 (cells E5 and X3):
// the trust check finds no repository there. With a daemon GIT_COMMON_DIR on
// git.info and git.list_branches the trust check pins nothing, and nothing is
// read (not measured).
//
// The gate makes no call to git about the owner of the repository. In the state
// shadow the call of shadowHost.askGit comes before it. That call answers a
// repository of another owner with the "no repository" answers, before any
// read. A refused read is not kept.
//
// On 5fd08069 a first request on a repository with a refused config makes the
// call about the repository and the reads, and each later request makes the
// reads again: 1 call in 19 cells, 4 with a chain of four included files and
// 17 with seventeen. It makes no `rev-parse` call of its own there (cells of
// group E and cells 10a to 10e of val-s3p2b2-3-linux, Linux VM. Rows D3 of
// val-s3p2b2-3-macos and D7 of val-s3p2b2-3-windows). After a read with a
// syntax error 5fd08069 makes one `git version` call (versionAfterFailure).
//
// A server with no run folder has no state and no call of askGit. It reads the
// config and answers its refusals the same way. No real daemon is such a
// server, so that form is not measured.
func repoConfigGate(t gitDirTrust, off jsonTrueSwitch) (msg string, accepted *repoVerdict) {
	if off || t.verdict != gitDirTrusted || t.pinCommonDir == "" {
		return "", nil
	}
	v := repoReadVerdict(t)
	if v == nil {
		return "", nil
	}
	if refusal := requestRefusal(t, v); refusal != "" {
		return refusal, nil
	}
	return "", v
}

// requestRefusal is the refusal of one request on the trust verdict t with the
// read v, or "": the refusal of the config, then that of the `config.worktree`
// file of a linked worktree, then that of another file of the git directory.
func requestRefusal(t gitDirTrust, v *repoVerdict) string {
	refusal := v.refusal
	if refusal == "" {
		if d := entryConfigDetail(v.worktreeConfig, t.worktreeEntry, nil); d != "" {
			refusal = repoConfigPrefix + d
		}
	}
	if refusal == "" {
		// The tests of shallow, info/attributes and info/exclude run in each
		// request and are no part of the kept read. On 5fd08069 a change of one of
		// these files leads to no new read of the config and to no new copy of it
		// (cells 19a to 19c of val-s3p2b2-3-linux, 15-19a to 15-19c of
		// val-s3p2b2-3-macos and E15b of val-s3p2b2-3-windows).
		refusal = gitDirFilesRefusal(t.pinCommonDir)
	}
	return refusal
}
