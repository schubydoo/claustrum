package git

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
	"time"

	"github.com/schubydoo/claustrum/internal/rpc"
	"github.com/schubydoo/claustrum/internal/sysproc"
)

// The daemon's own read of a repository's config, and its tests of three files of
// the git directory. 5fd08069 refuses a git method when it does not accept one of
// them. docs/protocol/git-rules-config.md, "Repository config refusals", gives the rules.
// A Linux system with git 2.43.0 ran every measurement that a comment names here,
// unless the comment names another system.

const (
	// repoConfigPrefix starts a refusal of the config, and repoFilePrefix one of
	// another file of the git directory.
	repoConfigPrefix = "this repository's git config cannot be used: "
	repoFilePrefix   = "a file in this repository's .git directory cannot be used: "

	// repoConfigMaxIncludes is the most included files that the daemon reads. 16
	// in a row pass, and 17 are refused (measured on Linux and macOS).
	repoConfigMaxIncludes = 16
	// repoConfigMaxDepth is the deepest include chain. config, d1, d2 and d3 pass,
	// and a fourth level is refused (measured on Linux and macOS).
	repoConfigMaxDepth = 3
	// repoConfigMaxBytes bounds one config file. 8388608 bytes pass, and 8388609
	// are refused (measured).
	repoConfigMaxBytes = 8 << 20
	// repoGitFileMaxBytes bounds info/attributes, info/exclude and shallow. 4194304
	// bytes pass, and 4194305 are refused (measured).
	repoGitFileMaxBytes = 4 << 20
	// repoConfigKeyMaxBytes bounds the full name of a setting, with its section and
	// subsection, in the sections of repoNameBoundSections. 1024 bytes pass, and
	// 1025 are refused, in core and remote (measured).
	repoConfigKeyMaxBytes = 1024
	// repoSettingCost and repoSettingsMaxCost make the "more settings" bound. The
	// arithmetic is in docs/protocol/git-rules-config.md, "The count of settings". It is fitted to
	// the measured pairs. TestRepoConfigSettingsCost has each pair.
	repoSettingCost     = 96
	repoSettingsMaxCost = 8 << 20
)

// configLinkTail ends the detail of a config that the daemon reaches through a
// link that it does not follow. 5fd08069 writes it on Windows for a config that
// is a file link (configLinkDetail) and for a git directory that is a junction
// (gitDirLinkDetail), measured on Windows.
const configLinkTail = ": is, or is reached through, a link that Claude does not follow here; " +
	"replace the link with what it points to"

// repoNameBoundSections are the sections whose setting names have the bound of
// repoConfigKeyMaxBytes. The exact pair is measured in core and remote, and in
// lfs, url, filter, extensions, include and includeif. A measurement measured
// the same pair in index, feature and checkout (measured on Linux, macOS and
// Windows). A name of 1100 or 2200 bytes passes in the sections user, alias,
// http, credential, safe, gc, diff, merge, x, abcde, branch and submodule, and
// so does a section name of 1100 bytes. A subsection of 1100 bytes passes in
// branch (measured). No other section was run.
var repoNameBoundSections = map[string]bool{
	"core": true, "extensions": true, "include": true, "includeif": true,
	"lfs": true, "url": true, "filter": true, "remote": true,
	"index": true, "feature": true, "checkout": true,
}

// repoCountedSections are the sections whose settings count toward
// repoSettingsMaxCost (measured). The exact pairs are in core and remote. For
// each other section here one cell of 200000 lines was refused. The sections x
// and user do not count. No other section was run: add a measured one here.
var repoCountedSections = map[string]bool{
	"core": true, "remote": true, "extensions": true, "include": true, "branch": true,
	"lfs": true, "index": true, "url": true, "filter": true, "feature": true,
	"checkout": true, "includeif": true,
}

// listConfigText runs `git config --no-includes --file - --list -z` with text as
// its standard input, and returns the listing. git follows no include in this
// call. dir and env are the working directory and the environment of the call:
// "" and nil keep those of the daemon.
//
// In a request with time limits the call ends at repoReadBound, with
// errReadBound (runConfigText).
func listConfigText(ctx context.Context, text []byte, dir string, env []string) ([]byte, error) {
	cmd := configTextCmd(ctx, dir, env)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := runConfigText(ctx, cmd, text)
	return out.Bytes(), err
}

// configTextCmd is the listing of one config file on standard input, in dir
// with env. The end of ctx ends it.
func configTextCmd(ctx context.Context, dir string, env []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", "config", "--no-includes", "--file", "-", "--list", "-z")
	cmd.Dir = dir
	cmd.Env = env
	return cmd
}

// errReadBound is the error of a listing of one config file that repoReadBound
// ended. A create quotes it after repoConfigPrefix (measured on Linux).
var errReadBound = errors.New("git did not finish reading it within 30 seconds; try again")

// runConfigText runs cmd with text as its standard input. In a request with
// time limits (gitStop) the call ends at repoReadBound, counted from its start,
// and startBoundDrain after it, with errReadBound (runStartBound).
func runConfigText(ctx context.Context, cmd *exec.Cmd, text []byte) error {
	if s, _ := gitStopOf(ctx); s == nil {
		cmd.Stdin = bytes.NewReader(text)
		return cmd.Run()
	}
	ended, err := runStartBound(cmd, text, repoReadBound)
	if ended {
		return errReadBound
	}
	return err
}

// runStartBound runs cmd with stdin as its standard input. cmd.Stdout and
// cmd.Stderr are writers or nil. One bound, counted from the start of the call,
// ends it: git gets SIGKILL with no SIGTERM. The daemon reads the pipes for up
// to startBoundDrain more, and then closes its ends of them. A child that still
// holds them gets no signal. ended reports that the bound ended the call.
//
// 5fd08069 answers about 31 s after the start also when git exited 0 earlier
// and only a child holds its stdout past the bound. The child gets no signal.
// The repository config read of git.status and call
// 7 of the start test then answer their refusal texts (measured on Linux). A
// call that the bound ended stays refused when the pipes close inside the
// drain (claustrum's own, not measured).
//
// git is reaped at the end of its stdout or at the bound, whichever comes first.
// Until then a git that exited stays a zombie. 5fd08069 reaps it at about 30 s
// (measured on Linux and macOS). After the reap the copy of stderr gets at most
// startBoundDrain. With a child that holds stderr alone, 5fd08069 goes on about
// 1.1 to 1.4 s after the exit of git and uses the listing (measured on Linux and
// macOS, and on Windows, about 1.0 to 1.2 s there). A call that exits non-zero
// while a child still holds stderr at that cut gives heldStderrExit.
func runStartBound(cmd *exec.Cmd, stdin []byte, bound time.Duration) (bool, error) {
	var files []*os.File
	for range 3 {
		r, w, err := os.Pipe()
		if err != nil {
			sysproc.CloseAll(files...)
			return false, err
		}
		files = append(files, r, w)
	}
	inR, inW, outR, outW, errR, errW := files[0], files[1], files[2], files[3], files[4], files[5]
	sinks := []io.Writer{cmd.Stdout, cmd.Stderr}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
	err := cmd.Start()
	sysproc.CloseAll(inR, outW, errW)
	if err != nil {
		sysproc.CloseAll(inW, outR, errR)
		return false, err
	}
	var mu sync.Mutex
	done, ended := false, false
	var drain *time.Timer
	atBound := make(chan struct{})
	timer := time.AfterFunc(bound, func() {
		mu.Lock()
		defer mu.Unlock()
		// The close comes after the kill, so Wait cannot reap git before it.
		defer close(atBound)
		if done {
			return
		}
		ended = true
		_ = cmd.Process.Kill()
		_ = inW.Close()
		drain = time.AfterFunc(startBoundDrain, func() { sysproc.CloseAll(outR, errR) })
	})
	go func() {
		_, _ = inW.Write(stdin)
		_ = inW.Close()
	}()
	var copies sync.WaitGroup
	outEOF := make(chan struct{})
	for i, r := range []*os.File{outR, errR} {
		w := sinks[i]
		if w == nil {
			w = io.Discard
		}
		copies.Go(func() {
			_, _ = io.Copy(w, r)
			_ = r.Close()
			if r == outR {
				close(outEOF)
			}
		})
	}
	select {
	case <-outEOF:
	case <-atBound:
	}
	err = cmd.Wait()
	cut := time.AfterFunc(startBoundDrain, func() { _ = errR.Close() })
	copies.Wait()
	var ee *exec.ExitError
	if !cut.Stop() && errors.As(err, &ee) {
		err = heldStderrExit{err}
	}
	mu.Lock()
	done = true
	stopped := ended
	if drain != nil {
		drain.Stop()
	}
	mu.Unlock()
	timer.Stop()
	_ = inW.Close()
	return stopped, err
}

// heldStderrExit is the error of a call of runStartBound that exited non-zero
// while a child held its stderr past startBoundDrain. It wraps the exec error.
type heldStderrExit struct{ err error }

func (e heldStderrExit) Error() string { return e.err.Error() }
func (e heldStderrExit) Unwrap() error { return e.err }

// repoListingMaxBytes bounds the listing that the read of the repository config
// takes from git for one file. A listing names the whole key of each setting, so
// a small file with a long subsection name and many keys lists as far more bytes
// than it has. By arithmetic, the largest accepted files list as no more than
// their own size. No cell measured a listing size. The bound is claustrum's own,
// and 5fd08069 is not measured with a listing over it. A variable, so that a
// test can lower it.
var repoListingMaxBytes = 8 * repoConfigMaxBytes

// repoReadBound ends each listing of one config file on standard input, in a
// request with time limits (gitStop, runConfigText). The bound counts from the
// start of the call. The process gets SIGKILL with no SIGTERM before it. The
// read of the repository config then answers readStopText after
// repoConfigPrefix. On 5fd08069 a git.status whose listing of `.git/config`
// was held 35 s answered at 30.3 s so (measured on Linux). The listings of a
// `config.worktree` file end so too (measured on Linux). A git that exited 0
// while a child holds its pipes past the bound gets the same in the read of
// the repository config, about 31 s after the start (measured on Linux). In
// the `config.worktree` listings and the entry test of git.status that is
// claustrum's own, not measured. macOS is measured. Windows, an included file
// and git.info are not measured: they get the same (claustrum's own). A
// variable, so that a test can lower it.
var repoReadBound = 30 * time.Second

// startBoundDrain is the time that runStartBound reads the pipes after its bound.
// 5fd08069 answers two measured cases about 1 s after the 30 s bound (measured on
// Linux). It is also the time that the copy of stderr gets after the reap. With a
// child that holds stderr alone, 5fd08069 goes on about 1.1 to 1.4 s after the exit
// of git (measured on Linux and macOS). The exact value of 1 s is claustrum's own.
var startBoundDrain = time.Second

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
	cmd := configTextCmd(ctx, dir, env)
	listing := &boundedListing{max: repoListingMaxBytes}
	errText := &sysproc.CappedText{Max: 1 << 16}
	cmd.Stdout = listing
	cmd.Stderr = errText
	err = runConfigText(ctx, cmd, text)
	out = listing.buf.Bytes()
	if listing.over {
		return out[:bytes.LastIndexByte(out, 0)+1], nil, errListingTooLarge
	}
	return out, errText.Kept(), err
}

// repoConfigListEnv is the environment of the listing of one config file: the
// filtered environment of the daemon (gitChildEnv), then GIT_DIR=<null device>,
// LC_ALL=C and LANGUAGE=C. The call log of 5fd08069 shows these three values on
// Linux, and GIT_DIR=NUL on Windows. claustrum passes the filtered environment
// whole (claustrum's own, not measured), so the daemon's GIT_CONFIG and
// GIT_CONFIG_PARAMETERS stay. The call runs in gitVersionDir.
func repoConfigListEnv() []string {
	return append(gitChildEnv(), "GIT_DIR="+os.DevNull, "LC_ALL=C", "LANGUAGE=C")
}

// stdinSyntaxError matches the text of git for a syntax error in a file that it
// reads on standard input.
var stdinSyntaxError = regexp.MustCompile(`(?m)^fatal: bad config line ([0-9]+) in standard input\s*$`)

// badLineDetail is the text of git for a syntax error of the file at path, with
// the path in quotes, or "" when errOut, the error output of a listing on
// standard input, has no such text.
func badLineDetail(errOut []byte, path string) string {
	if m := stdinSyntaxError.FindSubmatch(errOut); m != nil {
		return "fatal: bad config line " + string(m[1]) + " in " + quoteOperand(path)
	}
	return ""
}

// stderrLines is each line of errOut that is not empty, with its spaces at its
// ends cut, joined by one space. It is the join of allLines too.
func stderrLines(errOut []byte) string {
	var lines []string
	for line := range strings.SplitSeq(string(errOut), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, " ")
}

// openRepoFile opens path for a read. The open does not wait, so a FIFO with no
// writer is judged at once: 5fd08069 answers in under 350 ms there (measured). A
// file that is missing, a dangling link included, gives nil and no error
// (measured).
//
// The open never makes the file the controlling terminal of the daemon
// (openNoTerminal). With a config that is a link to a pty slave, 89cb6289 and
// 5fd08069 stay up when the pty master closes (measured on Linux, 6 of 6 runs).
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
// null device does not (measured). large is the end of the size refusal. The
// path is quoted and cut as quoteOperand does it: the first 300 characters stay,
// with no mark of the cut. Measurements show that cut for ASCII paths in the
// size text of the config, and with characters of two bytes.
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

// refused reports whether the extension is refused under format version 1: a
// name other than worktreeconfig that is not in copyKeyRules, or a value that
// its value rule does not accept. 5fd08069 refuses `preciousobjects = 1234567890`,
// `objectformat = sha512`, `refstorage` with no value and `partialclone = a/-b`
// (measured on Linux). A measurement measured more names and values.
func (e repoExtension) refused() bool {
	if e.name == "worktreeconfig" {
		return false
	}
	_, ok := shadowValue(repoSetting{key: "extensions." + e.name, value: e.value, hasValue: e.hasValue})
	return !ok
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
	// conditional are the settings of the files of `includeIf` sections, for
	// the filter rule only. The copy does not carry them. 5fd08069 refuses a
	// filter of such a file in the state shadow, also when its condition does
	// not hold (measured on Linux). claustrum follows no include in that file
	// (claustrum's own, not measured).
	conditional []repoSetting
	// ruleSkip holds the indices of settings that the filter rule passes
	// over: each `smudge` or `process` line of a filter that a later one of the
	// same file replaces (lastLinePerFile).
	ruleSkip map[int]bool
	// version is the last value of core.repositoryformatversion in the config
	// itself, when hasVersion.
	version    string
	hasVersion bool
	// extensions are those of the config itself.
	extensions []repoExtension
	// worktreeConfig is true when the bool rule (readBoolWord) reads the last
	// extensions.worktreeconfig setting of the config itself as true.
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
// the detail of a refusal, or "". Each file is one git call (measured: 17 such
// calls for the config and 16 included files). An `include.path` setting is
// followed where it stands. An empty value, and a setting with no value, name no
// file (measured).
//
// Three tests are for the config itself, depth 0, and not for an included file: the
// format version, the extensions and the worktreeConfig switch. A version `2` and an
// `extensions.foo` in an included file are not refused (measured). The name bound
// and the "more settings" bound hold in an included file too, with the path of that
// file (measured on Linux, macOS and Windows). The count is one sum across the
// included files, and the config has its own sum. Two included files of 40000
// `core.filemode = true` lines each are refused, and the text names the second file.
// The config with 40000 such lines and one such included file passes (measured on
// Linux, macOS and Windows). A Linux test ran that pair in both orders. A file below
// an included file adds to the same sum: a chain of three files of 30000 such lines
// is refused, and the text names the third file. A chain of two passes (measured on
// the same three systems).
//
// An open that fails is a refusal with the error text of the open and no quotes:
// a socket and a file of mode 000, as the config (measured) and as an included
// file (measured on Linux, and mode 000 on macOS). A syntax error has the text of
// git with the path in quotes, in the config (measured) and in an included file
// (measured on Linux, macOS and Windows).
//
// Both bounds are tested on the whole file before an include of it is followed.
// That order is fitted to two cells: 200000 `include.path` lines of a missing
// file (measured), and 200000 such lines in an `includeIf` section (measured on
// Linux). Both get the "more settings" text, not the include limit.
//
// Each `include.path` setting with a value counts toward the 16 files, also when
// its file is missing: 17 missing files are refused (measured on Linux, macOS and
// Windows), and 16 pass (measured on Linux and Windows). The limits come before
// the open of the file (not measured apart from the missing file).
//
// The file of an `includeIf` section counts toward the 16, and it gets the kind
// test and the size test, whatever the condition is: `gitdir:` that matches or
// not, `gitdir/i:`, `onbranch:` and `hasconfig:` (measured on Linux, macOS and
// Windows). The value tests of includeTarget run too: `~root/x` gets the
// "expands neither" text in a matching section (measured) and in one that does
// not match (measured on Linux). A missing file of such a section counts toward
// the 16 (measured on Linux, macOS and Windows). 5fd08069 lists such a file with
// one more git call, with or without a match, and carries no setting of it
// (measured on Linux and macOS). claustrum makes that call. It keeps its answer
// only for the filter rule (repoConfigRead.conditional). A file there that git
// cannot list is not measured.
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
	if errors.Is(err, errReadBound) {
		return readStopText(path)
	}
	// A listing over its bound still gets the name bound and the count of the
	// settings that the read kept. Those two refusals are the first in the order
	// of the listing, so they are the ones of the whole listing. With neither,
	// the read has no verdict.
	over := errors.Is(err, errListingTooLarge)
	if err != nil && !over {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if d := badLineDetail(errOut, path); d != "" {
				// 5fd08069 runs one `git version` after this read
				// (versionAfterFailure). An included file with a syntax error is
				// not measured: it gets the call too.
				versionAfterFailure()
				return d
			}
			// A listing that exits non-zero while a child holds its stderr is
			// refused with the lines read up to the cut of the stderr copy, and
			// one `git version` call follows. 5fd08069 answers so (measured on
			// Linux and macOS). A failed listing with no holder keeps the rule
			// before it (claustrum's own, not measured).
			if errors.As(err, new(heldStderrExit)) {
				if d := stderrLines(errOut); d != "" {
					versionAfterFailure()
					return d
				}
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
			// The bool rule reads the switch. `true`, `yes`, `1`, `TRUE`, `on`,
			// `ON`, `Yes`, `On` and `tRuE` turn it on, and `false`, `no` and `0`
			// do not (measured on Linux, macOS and Windows). `2` and `10` turn
			// it on (measured on Linux).
			v, ok := readBoolWord(repoSetting{value: value, hasValue: hasValue})
			r.worktreeConfig = ok && v == "true"
		}
		if name, ok := strings.CutPrefix(key, "extensions."); ok {
			r.extensions = append(r.extensions, repoExtension{name: name, value: value, hasValue: hasValue})
		}
	}
	if over {
		r.unlisted = true
		return ""
	}
	lineAt := map[string]int{}
	for rec := range bytes.SplitSeq(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		key, value, hasValue := strings.Cut(string(rec), "\n")
		r.settings = append(r.settings, repoSetting{key: key, value: value, hasValue: hasValue, included: depth > 0})
		if isLastLineKey(key) {
			if at, ok := lineAt[key]; ok {
				if r.ruleSkip == nil {
					r.ruleSkip = map[int]bool{}
				}
				r.ruleSkip[at] = true
			}
			lineAt[key] = len(r.settings) - 1
		}
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
			if settings, ok := listConfigFile(r.ctx, target); ok {
				r.conditional = append(r.conditional, lastLinePerFile(settings)...)
			}
		}
	}
	return ""
}

// isLastLineKey reports whether key is a `smudge` or a `process` key of a
// filter, as git lists it: `filter.<name>.smudge` or `filter.<name>.process`.
func isLastLineKey(key string) bool {
	return strings.HasPrefix(key, "filter.") && (len(key) > len("filter..smudge") && strings.HasSuffix(key, ".smudge") ||
		len(key) > len("filter..process") && strings.HasSuffix(key, ".process"))
}

// lastLinePerFile is settings, the settings of one file, with only the last `smudge` and the last
// `process` line of each filter. The filter rule judges the last value of each file. 5fd08069 passes
// `smudge = cat` after another value in .git/config, and refuses the reverse order (measured on
// Linux). It refuses a value that does not pass in .git/config and `cat` in an includeIf file, in
// either order (measured on Linux). The last value of an includeIf file, of a plain include file and
// of config.worktree is claustrum's own, not measured. For a `process` line it follows measured
// cases of another step (not measured here).
func lastLinePerFile(settings []repoSetting) []repoSetting {
	last := map[string]int{}
	for i, s := range settings {
		if isLastLineKey(s.key) {
			last[s.key] = i
		}
	}
	var out []repoSetting
	for i, s := range settings {
		if at, ok := last[s.key]; !ok || at == i {
			out = append(out, s)
		}
	}
	return out
}

// listConfigFile lists the config file at path with one git call, as the read
// lists the config. ok is false, with no call, for a file that is missing, that
// is not a regular file or that is over repoConfigMaxBytes. It is false too
// when git did not list the file. The open does not wait (openRepoFile).
func listConfigFile(ctx context.Context, path string) (settings []repoSetting, ok bool) {
	_, settings, _, ok = listConfigFileText(ctx, path)
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
// file of 20 MiB the size text, each with the path of that file (measured). A
// version `2` in such a file is not refused, and no other content of it ran. An
// open error there is not measured: claustrum gives no refusal.
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
// file, also when it leaves the git directory (measured), and `~/` is the `HOME`
// folder of the daemon. `~user` and `%(prefix)/` are refused (measured with
// `~root/x` and `%(prefix)/x`). A `~/` value with an unset or relative `HOME` gets
// the same refusal: the text says so, and no cell ran it. The Windows rules come
// first (includeValueDetail and includePlaceDetail).
//
// The value in the text is cut to its first 100 characters: a value of 100 is
// whole, and one of 101 is cut, also with characters of two bytes (measured
// on Linux and macOS).
//
// The path of a relative include is the folder of file, one separator, and the
// value as written. It is not cleaned: on Windows `path = <dir>/inc.cfg` gives `<git
// dir>\<dir>/inc.cfg` in a text (measured). No cell on Linux or macOS has a value that a
// clean changes. The folder of file stays as written too: `include.path = a/../b/inc.cfg`,
// and in that file `path = x.cfg`, gives `<git dir>/a/../b/x.cfg` in a text (measured on
// Linux and macOS). The `~/` form is joined and cleaned as before (not measured).
// `include.path = ~` names the home folder itself, and gets the "not a regular file" text
// (measured on Linux).
func includeTarget(file, value, gitDir string) (target, detail string) {
	if d := includeValueDetail(file, value); d != "" {
		return "", d
	}
	unexpanded := quoteOperand(file) + " includes " + quoteCut(value, includeValueMaxRunes) +
		", and Claude expands neither ~user nor %(prefix), nor ~ where no home directory is set; write that path out in full"
	switch {
	case value == "~" || strings.HasPrefix(value, "~/"):
		// HOME is the home folder here, claustrum's own, not measured.
		// An unset or relative HOME refuses. An empty HOME is not
		// relative, so `~/x` becomes `/x`. A bare `~` with an empty HOME is not
		// measured: it gives an empty path. The Unix form of relativeFor is right
		// here: includeValueDetail refuses every `~` path on Windows first.
		home, set := os.LookupEnv("HOME")
		if !set || relativeFor(home, false) {
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
// The format version is the last value of core.repositoryformatversion in the config
// itself, as git lists it. It must be exactly the text 0 or 1 (measured). A config with
// no such setting is not refused (measured). Under version 1, the first extension of the
// config itself that is refused gives its name (repoExtension.refused). Under version 0
// no extension is refused (measured). The version test comes before the extension test
// (not measured). An extension in a config with no version key is not refused (measured
// with `foo = true` on Linux, macOS and Windows).
//
// With `extensions.worktreeConfig` in the config, true by the bool rule, the `config.worktree` file of
// entry gets the kind test and the size test. Measurements ran git.info with a linked worktree as its
// path, and git.status, git.list_branches and git.worktree_create too (measured on Linux and Windows), and
// a remove (measured on Linux). The `config.worktree` file of the main worktree gets its tests in
// readRepoConfig (worktreeConfigFileDetail).
//
// The version value in its text is cut to its first 20 characters, with no mark
// of the cut. The extension name is cut to its first 60. A value of 20
// characters is whole, and one of 21 shows 20. A name of 60 is whole, and one of
// 61 shows 60 (measured on Linux, macOS and Windows). The cells have ASCII text.
// claustrum counts characters, as for a path.
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
// (measured on Linux). claustrum lists it two times and takes the settings of
// the first listing. With an extension refusal both list it once (measured on
// Linux). A syntax error there is refused (measured). Before any git call reads
// it the file gets the kind test, the size test and the open of the config
// (worktreeConfigFileDetail). A file that git cannot list for another reason is
// not measured: its settings are not used and nothing is refused.
//
// A file of 0 bytes is not listed. On 5fd08069 the first git.info then makes 21
// calls, with no read of that file (measured on Linux). The create of that cell
// still lists the file after its `worktree add` (newWorktreeConfig).
func readRepoConfig(ctx context.Context, gitDir string, log *repoReadLog) (*repoConfigRead, string) {
	r := &repoConfigRead{ctx: ctx, gitDir: gitDir, log: log}
	if d := gitDirLinkDetail(gitDir); d != "" {
		return r, d
	}
	// A git directory that is a file with no `gitdir:` line is refused here, with
	// the error of its open as a folder. 5fd08069 answers so on Windows for a daemon
	// GIT_DIR that names such a file, on the five methods (measured). A `.git` file
	// of a linked worktree passes here and gets the class 2 refusal
	// (gitDirFilesDetail). The `gitdir:` test that tells the two apart is
	// claustrum's own choice.
	if _, isGitFile := readGitFile(gitDir, filepath.Dir(gitDir)); !isGitFile {
		if d := gitDirNotFolderDetail(gitDir); d != "" {
			return r, d
		}
	}
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
	worktreeListed := ""
	if r.worktreeConfig {
		path := filepath.Join(gitDir, "config.worktree")
		log.note(path)
		// A syntax error there refuses the request with the text of git (measured on
		// Linux). It gets the `git version` call of a config with such an error. It
		// comes before the extension test (measured on Linux). Other failures of the
		// listing are not measured. A good file gets its second listing after the
		// extension test, so an extension refusal comes after one listing of that file
		// (measured on Linux).
		if d := worktreeConfigFileDetail(path, log); d != "" {
			return r, d
		}
		if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() || fi.Size() != 0 {
			_, settings, badLine, ok := listConfigFileText(ctx, path)
			if badLine != "" {
				versionAfterFailure()
				return r, badLine
			}
			if ok {
				r.worktree = settings
				worktreeListed = path
			}
		}
	}
	if r.version == "1" {
		for _, e := range r.extensions {
			if e.refused() {
				return r, "the repository uses git extension " + quoteCut(e.name, extensionNameMaxRunes) +
					", or a value of it, that this version of Claude does not support"
			}
		}
	}
	if worktreeListed != "" {
		listConfigFile(ctx, worktreeListed)
	}
	return r, ""
}

// worktreeConfigFileDetail tests the `config.worktree` file of the git
// directory at path, with the switch on. The open does not wait (openRepoFile).
// A missing file passes. 5fd08069 refuses these on git.worktree_create and
// git.info, before any add (measured on Linux and macOS):
//
//   - A FIFO, an empty folder and a link to the null device: `"<path>" is not a
//     regular file` (measured). A FIFO also on git.status and git.list_branches
//     (measured).
//   - A file that cannot be opened: the error of the open, `open <path>:
//     permission denied` for mode 000 (measured).
//   - A file over 8 MiB: the size text of a config (measured).
//
// Other kinds and the other methods get the same tests (from the code). A
// dangling link passes, and so does a link to a file (measured).
func worktreeConfigFileDetail(path string, log *repoReadLog) string {
	f, err := openRepoFile(path)
	if err != nil {
		log.fail()
		return err.Error()
	}
	if f == nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	return repoFileKindDetail(f, path, repoConfigMaxBytes, configLarge)
}

// gitDirFilesDetail tests shallow, info/attributes and info/exclude of the git
// directory gitDir, in that order, and returns the detail of a class 2 refusal,
// or "". With two refused files 5fd08069 names `shallow` before info/attributes
// (measured on Linux, macOS and Windows) and info/attributes before
// info/exclude. Measurements ran that pair four ways on Linux: a file of 20 MiB
// with a FIFO, two files of 20 MiB, two FIFOs, and a FIFO with a directory. A
// refused config comes before each of them (measured).
//
// A git directory that is a file gets the error of its open as a folder first. On
// Windows 5fd08069 answers so for a daemon GIT_DIR=.git and a linked worktree, on
// git.info, git.status, git.list_branches and git.worktree_create (measured).
// git.worktree_remove gives it in its lock-check text (measured). Other systems
// are not measured.
func gitDirFilesDetail(gitDir string, log *repoReadLog) string {
	if d := gitDirNotFolderDetail(gitDir); d != "" {
		return d
	}
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

// gitDirNotFolderDetail is the error of the open of gitDir as a folder when gitDir
// exists and is not a directory, or "".
func gitDirNotFolderDetail(gitDir string) string {
	if fi, err := os.Stat(gitDir); err != nil || fi.IsDir() {
		return ""
	}
	root, err := os.OpenRoot(gitDir)
	if err != nil {
		return err.Error()
	}
	_ = root.Close()
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
// not open the file. A FIFO and a directory are refused as not regular
// (measured), and a regular file of more than repoGitFileMaxBytes too. A symbolic
// link passes, also one to the null device and a dangling one, and so does a file
// of mode 000 (measured). A link to a FIFO or to a large file is not measured: it
// passes here, as every link does.
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
// as not regular (measured). A file of mode 000 gets the error text of the
// open, with no quotes (measured), and so does a socket. Another open error
// gets the same form (not measured). A dangling link and a missing file pass.
// The size text has no `: it` (measured). The file holds object ids and nothing
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
// before or after an id, are refused (measured). So are a line of 39, 41 or 63
// hex characters, an id with a space after it, and a file that holds one line end
// and nothing else, LF or CRLF (measured on Linux, macOS and Windows). This rule
// is fitted to those cells.
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
// repository no second time (measured on Linux).
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
	// shared is read from the last `core.sharedrepository` line of the copy
	// (sharedRepositoryOf, makeWorktreesFolder).
	shared sharedRepository
	// ownConfig are the `filter.` records of the settings, of the files of the
	// `includeIf` sections and of the `config.worktree` settings
	// (settingRecords), for the filter rule.
	ownConfig []byte
	states    []repoFileState
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
// (measured).
//
// On 5fd08069 a second git.info on a repository with an accepted config starts no
// git call for the read (measured). A request after a change of the config starts
// one (measured). With a refused format version each later request of that build
// makes the read again (measured on Linux, macOS and Windows). A kept read with a
// refusal is not used: claustrum reads again, too. claustrum keeps the verdict of a
// read with the state of each file that the read used. It reads again when one of
// them has another identity, size, modification time or mode. It reads again when a
// file that was missing is there. That rule is claustrum's own: a measurement ran
// one repeat and one changed config. A change that keeps all four values is not
// seen. A read with no verdict is not kept. A read in which an open or a read ended
// with an error is not kept either (repoReadLog.failed): its verdict answers this
// request, and the next request reads again. So a refusal with an `open <file>:`
// text is read again on each request. That rule is claustrum's own.
//
// s holds the time limits of the request, or is nil. With s the listing of each
// config file ends at repoReadBound (repoConfigRead.file).
func repoReadVerdict(t gitDirTrust, s *gitStop) *repoVerdict {
	// The link test of the git directory comes before a kept read. The kept
	// read tests the state of each file, and a link to the same folder keeps
	// those states. The refusal is not kept. A link that is there before the
	// first request gets the same refusal on 5fd08069 (measured on Windows).
	if d := gitDirLinkDetail(t.pinCommonDir); d != "" {
		return &repoVerdict{refusal: repoConfigPrefix + d}
	}
	home, _ := os.UserHomeDir()
	k := repoVerdictKey{gitDir: t.pinCommonDir, home: home}
	if v, ok := keptRepoVerdict(k); ok && v.refusal == "" {
		return v
	}
	var log repoReadLog
	v := &repoVerdict{}
	r, d := readRepoConfig(s.context(), t.pinCommonDir, &log)
	switch {
	case d != "":
		v.refusal = repoConfigPrefix + d
	case log.noVerdict:
		return nil
	default:
		v.worktreeConfig = r.worktreeConfig
		v.shadowText = shadowConfigText(r.settings, r.worktree)
		v.worktreeItems = worktreeConfigItems(r.worktree)
		var own []repoSetting
		for i, s := range r.settings {
			if !r.ruleSkip[i] {
				own = append(own, s)
			}
		}
		v.ownConfig = settingRecords(own, r.conditional, lastLinePerFile(r.worktree))
		for _, s := range r.settings {
			if line, ok := shadowValue(s); ok && s.key == "core.sharedrepository" {
				v.shared = sharedRepositoryOf(line)
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
// as the config gets no answer in 10 s, as on 89cb6289. Measurements show that
// on all five methods (Linux and macOS, and Windows with no FIFO). So nothing is
// read then.
//
// It runs for a trusted git directory only. A folder whose HEAD is no ref keeps
// its "not a repository" answers, also with format version 2 (measured): the
// trust check finds no repository there. With a daemon GIT_COMMON_DIR on
// git.info and git.list_branches the trust check pins nothing, and nothing is
// read (not measured).
//
// The gate makes no call to git about the owner of the repository. In the state
// shadow the call of ShadowHost.askGit comes before it. That call answers a
// repository of another owner with the "no repository" answers, before any
// read. A refused read is not kept.
//
// On 5fd08069 a first request on a repository with a refused config makes the
// call about the repository and the reads, and each later request makes the
// reads again: 1 call in 19 cells, 4 with a chain of four included files and
// 17 with seventeen. It makes no `rev-parse` call of its own there (measured
// on Linux, macOS and Windows). After a read with a syntax error 5fd08069
// makes one `git version` call (versionAfterFailure).
//
// A server with no run folder has no state and no call of askGit. It reads the
// config and answers its refusals the same way. No real daemon is such a
// server, so that form is not measured.
func repoConfigGate(t gitDirTrust, off rpc.JSONTrueSwitch, s *gitStop) (msg string, accepted *repoVerdict) {
	if off || t.verdict != gitDirTrusted || t.pinCommonDir == "" {
		return "", nil
	}
	v := repoReadVerdict(t, s)
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
		// (measured).
		refusal = gitDirFilesRefusal(t.pinCommonDir)
	}
	return refusal
}
