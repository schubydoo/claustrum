package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/schubydoo/claustrum/internal/logging"
)

// The content check of git.worktree_create. After a good checkout b3081b51
// compares the files of the new worktree under .claude, .mcp.json, CLAUDE.md,
// CLAUDE.local.md and AGENTS.md with the commit. A mismatch refuses the create
// with a result that carries one of five codes, and the rollback of a failed
// checkout undoes the worktree (measured on Linux, macOS and Windows).
//
// The steps and their order are claustrum's own. The rows show the git calls,
// their working folders, the codes and the texts. The calls follow c9125eb8:
//
//   - `cat-file --batch`, or `cat-file commit <id>` and `cat-file tree <id>`,
//     read the commit and each tree on the way to the files (catFile), with
//     the base repository as the working folder. A long-running `hash-object
//     -t <type> --literally --no-filters --stdin-paths -w`, or `hash-object -t
//     <type> --literally --no-filters --stdin -w`, hashes each one again
//     (worktreecontentbatch.go). Its objects go to a short-lived folder
//     `.readback-objects-<n>` of the folder of the daemon (requestDir). The
//     long-running one reads each object from a file in a second such folder,
//     `.readback-scratch-<n>` (scratchDir).
//   - `hash-object --no-filters --stdin-paths` hashes the files of the new
//     worktree. A file whose id differs, or every file after a failed call, is
//     compared with `cat-file blob <id>`, and then with `cat-file --filters
//     --path=<name> <id>` in the new worktree. The blob is hashed again with
//     `hash-object -t blob --literally --no-filters --stdin -w` right after its
//     read (c9125eb8, measured on Linux and macOS).
//
// Each call has the limit of 2 minutes of a git call (gitCallLimit), with the
// stop of gitRun: SIGTERM to the git process alone, and SIGKILL 2 s later, on
// Linux and macOS. On Windows the process ends at once. A stopped call stops
// no other call of the request (contentCheck.git).
type contentCheck struct {
	u      gitUse
	repo   string
	leaf   string
	commit string
	// objects is the folder of the objects that the hash-object calls write.
	objects string
	// scratch is the root of the folder of the files that the long-running
	// hash-object calls read (scratchDir), or nil before the first one.
	// dropScratch removes the folder.
	scratch     *os.Root
	dropScratch func()
	// read is the count of objects that the check read: the commit, each tree
	// and each blob of the compare step.
	read int
	// topNames are the names of the top tree of the commit.
	topNames []string
	// linkBlobs are the blobs of the committed links, by path.
	linkBlobs map[string][]byte
	// fileModeOff is a core.fileMode of false, as git reads it in the base
	// repository (noteFileMode).
	fileModeOff bool
	// batch is the running `cat-file --batch` of the check, or nil.
	batch *objectBatch
	// hashers are the running long-running hash-object calls, by object kind.
	hashers map[string]*objectBatch
	// stopped is true after a long-running git of the check stopped. The batch
	// and the hash-object calls then take no more objects, and each level of
	// trees below the top is read per folder depth (readLevel).
	stopped bool
	// depthEnded is true after a read per folder depth gave an answer that
	// does not parse or does not come, or after its tree hash-object stopped.
	// No later level is read per folder depth then (readLevel).
	depthEnded bool
	// separate is the count of commits and trees that separate `cat-file`
	// runs read.
	separate int
	// ask asks git about the repository before the start of the batch and
	// before each separate `cat-file` run (askRepo), or is nil. It returns the
	// text of a bad answer, or "".
	ask func() string
	// asked is true after ask gave a bad answer.
	asked bool
	// differ are the files of the compare step, in its order.
	differ []contentFile
	// lfsFiles are the paths of differ whose filter attribute is lfs, or nil
	// before the first ask (lfsFiltered).
	lfsFiles map[string]bool
	// deadline is the context of the timeoutMs of the caller, or nil for none.
	// The time of the LFS keep, keepTook, does not count (keepWithinDeadline,
	// claustrum's own).
	// atDeadline is true once the check saw the deadline pass (pastDeadline).
	deadline   context.Context
	keepTook   time.Duration
	atDeadline bool
	// kills holds the helpers that the deadline kills, or is nil
	// (armDeadlineKill).
	kills *helperKills
}

// errContentDeadline ends the reads of a check whose deadline passed.
var errContentDeadline = errors.New("the deadline of the request passed")

// pastDeadline reports whether the deadline passed. Then the check reads no object,
// also by a separate run (4c111e86, measured on Linux for a batch that did not answer).
// Apart from the kill of four helpers (helperKills), a deadline during another git call
// does not cut that call. The check stops at the next step (4c111e86 and 87375cc7, measured on
// Linux, and 4c111e86 on Windows).
func (c *contentCheck) pastDeadline() bool {
	if !c.atDeadline && c.deadline != nil && (c.kills.sent() || callerTimeoutFired(c.deadline) &&
		!keepWithinDeadline(c.deadline, c.keepTook, time.Now())) {
		c.atDeadline = true
	}
	return c.atDeadline
}

// readAtDeadline is b.read, which the deadline ends: then the pipes of b
// close, and the read gives an error (objectBatch.drop). A stop of the
// context for another cause does not end it.
func (c *contentCheck) readAtDeadline(b *objectBatch, kind, id string) ([]byte, bool, error) {
	if c.deadline == nil {
		return b.read(kind, id)
	}
	ctx, keep := c.deadline, c.keepTook
	readDone := make(chan struct{})
	defer close(readDone)
	kills := c.kills
	stop := context.AfterFunc(ctx, func() {
		if !callerTimeoutFired(ctx) || !waitForKeep(ctx, keep, readDone) {
			return
		}
		// The kill comes before the close of stdin (measured on Linux).
		kills.fire()
		b.drop()
	})
	defer stop()
	return b.read(kind, id)
}

// contentRefusal is the code and the hint of a refused content check. asked is
// true when the check got a bad answer of git about the repository.
type contentRefusal struct {
	ErrorCode string
	hint      string
	asked     bool
}

// contentRefusalStart starts the error text of every refusal of the check
// (measured on Linux, macOS and Windows).
const contentRefusalStart = "Claude could not confirm that the new worktree matches its commit, so it did not keep it. "

// contentLogHintCap is the most bytes of the refusal text that the log line of the
// check quotes. A text of 406 bytes keeps 400 on 4c111e86 (measured on macOS).
// Linux and Windows are claustrum's own.
const contentLogHintCap = 400

// shadowReadbackDirPrefix starts the name of the folder of the objects that the
// check writes (measured on Linux). readbackTempPrefix is its prefix in the
// temporary folder, claustrum's own: the check runs in the state shadow only.
//
// shadowScratchDirPrefix starts the name of the folder of the files that the
// long-running hash-object calls read (4c111e86, measured on Linux, macOS and
// Windows). scratchTempPrefix is its prefix in the temporary folder
// (claustrum's own).
const (
	shadowReadbackDirPrefix = ".readback-objects-"
	readbackTempPrefix      = "claude-ssh-readback-objects-"
	shadowScratchDirPrefix  = ".readback-scratch-"
	scratchTempPrefix       = "claude-ssh-readback-scratch-"
)

// contentTopNames are the files at the top of the commit that the check
// compares. .mcp.json matches without regard to case, as in
// claudeConfigPathspecs. The other three match exactly: with CLAUDE.md and
// claude.md in a commit, b3081b51 logs 4 files on macOS (measured). The case
// rule of .mcp.json and the name CLAUDE.local.md are claustrum's own (not
// measured).
var contentTopNames = []string{".mcp.json", "CLAUDE.md", "CLAUDE.local.md", "AGENTS.md"}

// contentFile is one file of the commit that the check compares.
type contentFile struct {
	path string // the path in the tree, with "/"
	mode string
	id   string
}

// checkWorktreeContent runs the content check of a new worktree at leaf,
// whose checkout read commit. It logs one line and returns nil on a match. ask
// is contentCheck.ask, and deadline and keepTook are those of contentCheck. A
// check that stops at the deadline returns nil and atDeadline: the caller then
// runs only the config listing of the copy step and answers the timeout
// (dotClaudeListingOnly).
func checkWorktreeContent(u gitUse, repo, leaf, commit string, ask func() string, deadline context.Context, keepTook time.Duration) (refused *contentRefusal, atDeadline bool) {
	start := time.Now()
	c := &contentCheck{u: u, repo: repo, leaf: leaf, commit: commit, ask: ask, deadline: deadline, keepTook: keepTook}
	files, refusal := c.run()
	if refusal != nil {
		refusal.asked = c.asked
	}
	short := commit
	if len(short) > 12 {
		short = short[:12]
	}
	word := "files"
	if len(files) == 1 {
		word = "file"
	}
	// The level INFO is claustrum's own (not measured).
	// The count of separate runs comes after the commit id (c9125eb8, measured
	// on Linux, macOS and Windows).
	line := fmt.Sprintf("[GitHandler] worktree_create: checked %d %s under .claude, .mcp.json, CLAUDE.md (or CLAUDE.local.md) and AGENTS.md against %d objects of commit %s (%d of the commits and folder listings read by separate git runs) in %s: ",
		len(files), word, c.read, short, c.separate, time.Since(start).Round(time.Millisecond))
	if c.atDeadline {
		logging.Infof("%sstopped at the deadline", line)
		return nil, true
	}
	if refusal != nil {
		logging.Infof("%srefused: %q", line, loggedHint(refusal.hint))
		return refusal, false
	}
	logging.Infof("%smatch", line)
	return nil, false
}

// loggedHint is the refusal text as the log line of the check quotes it. It has no
// final period (measured on Linux). It keeps its first contentLogHintCap bytes
// (4c111e86, measured on macOS). A rune that the cut splits goes (claustrum's own).
func loggedHint(hint string) string {
	hint = strings.TrimSuffix(hint, ".")
	if len(hint) > contentLogHintCap {
		hint = strings.ToValidUTF8(hint[:contentLogHintCap], "")
	}
	return hint
}

// run is the check. files are the files of the commit that it found and
// hashed, links left out.
func (c *contentCheck) run() (files []contentFile, refusal *contentRefusal) {
	dir, remove, err := c.u.requestDir(shadowReadbackDirPrefix, readbackTempPrefix)
	if err != nil {
		// A folder that cannot be made is not measured: the check refuses
		// with the repository code (claustrum's own).
		return nil, c.repositoryRefusal(err.Error())
	}
	defer remove()
	c.objects = dir
	// The scratch folder goes before the folder of the objects (4c111e86,
	// measured on Linux). Before the deadline it goes after the gits of the
	// check end (claustrum's own). At the deadline the check ends with no wait
	// for its `cat-file --batch` (4c111e86, measured on Linux), and the folder
	// goes then. On Windows it goes after the wait for the killed helpers
	// (armDeadlineKill). Both are claustrum's own, not measured.
	defer c.removeScratch()
	defer c.armDeadlineKill()()
	c.startBatch()
	defer c.stopBatch()
	body, text := c.readObject("commit", c.commit)
	if text != "" {
		return nil, c.repositoryRefusal(text)
	}
	tree, ok := treeOfCommit(body)
	if !ok {
		return nil, c.repositoryRefusal("the commit names no tree") // not measured
	}
	files, text = c.walk(tree)
	if text != "" {
		return nil, c.repositoryRefusal(text)
	}
	files = c.dropSkippedFiles(files)
	// The blob of each committed link is read first, and it counts in the log
	// line (measured on Linux and Windows). A link of the worktree goes on no
	// hash-object list, and the log line counts the other files only
	// (measured on Linux). A committed link that the checkout wrote as a
	// regular file is hashed and counted as one (measured on Windows). hashed
	// are those files.
	var hashed []contentFile
	for i, f := range files {
		if f.mode != "120000" {
			hashed = append(hashed, f)
			continue
		}
		blob, stderr, err := c.catFile("blob", f.id)
		if err != nil {
			return nil, c.repositoryRefusal(failText(stderr, err))
		}
		c.read++
		if c.linkBlobs == nil {
			c.linkBlobs = map[string][]byte{}
		}
		c.linkBlobs[f.path] = blob
		if fi, err := os.Lstat(filepath.Join(c.leaf, filepath.FromSlash(f.path))); err == nil && fi.Mode().IsRegular() {
			files[i].mode = "100644"
			hashed = append(hashed, files[i])
		}
	}
	// A link that leads to the top of the new worktree is refused (measured on
	// Linux, macOS and Windows). A link to a folder outside is not. Before that
	// test, on Linux and macOS, a link whose chain runs in a circle or through
	// /proc or /dev/fd is refused (b3081b51, measured). A chain of more than 8
	// links inside the new worktree is refused too (measured on macOS). The
	// order of the two tests is claustrum's own (not measured).
	for _, f := range files {
		if f.mode != "120000" {
			continue
		}
		switch committedLinkFault(c.leaf, f.path) {
		case linkTooLong:
			return nil, &contentRefusal{ErrorCode: "worktree_verify_link", hint: fmt.Sprintf(
				"Shorten the chain of links that starts at %q in this repository, or remove the link that leads back: it runs through more than %d links, or in a circle, further than this check follows (commit %s).",
				f.path, contentLinkLimit, c.short())}
		case linkThroughProc:
			return nil, &contentRefusal{ErrorCode: "worktree_verify_link", hint: fmt.Sprintf(
				"Point the link %q, or the link it leads through, somewhere that does not pass through /proc or /dev/fd (directly or by a link): what a path there names depends on the program reading it, so what Claude Code would read through it cannot be checked (commit %s).",
				f.path, c.short())}
		}
		if leadsToTop(c.leaf, f.path) {
			return nil, &contentRefusal{ErrorCode: "worktree_verify_link", hint: fmt.Sprintf(
				"Point the link %q, or the link it leads through, at a folder inside the repository instead of its top: everything in the repository could be read through it unchecked (commit %s).",
				f.path, c.short())}
		}
	}
	// A file that cannot be read, and a name of the check that the commit does
	// not have, refuse before the hash-object call, and the log line counts no
	// file (measured on Windows).
	if r := c.changedWhileChecked(hashed); r != nil {
		return nil, r
	}
	if r := c.otherWriter(); r != nil {
		return nil, r
	}
	if r := c.lostExecBit(hashed); r != nil {
		return nil, r
	}
	ids, failed := c.worktreeIDs(hashed)
	// The result of a hash that the deadline passed during is not used, and the
	// line counts no file (4c111e86 and 87375cc7, measured on Linux). A failed
	// call at that point is claustrum's own, not measured.
	if len(hashed) > 0 && c.pastDeadline() {
		return nil, nil
	}
	if failed {
		// A failed hash-object call whose files changed kind refuses with no
		// cat-file blob call (measured on Linux). The log line then counts no
		// file.
		if r := c.changedWhileChecked(hashed); r != nil {
			return nil, r
		}
	}
	// A link goes straight to the compare (measured on Linux). n is the index
	// of a hashed file in ids.
	n := 0
	for _, f := range files {
		if f.mode != "120000" {
			n++
			if ids != nil && ids[n-1] == f.id {
				continue
			}
		}
		c.differ = append(c.differ, f)
	}
	for _, f := range c.differ {
		if r := c.compare(f); r != nil {
			return hashed, r
		}
	}
	return hashed, nil
}

func (c *contentCheck) short() string {
	if len(c.commit) > 12 {
		return c.commit[:12]
	}
	return c.commit
}

func (c *contentCheck) repositoryRefusal(detail string) *contentRefusal {
	return &contentRefusal{ErrorCode: "worktree_verify_repository", hint: fmt.Sprintf(
		"Run \"git fsck\" in %q to check the repository: git couldn't read what it stores for commit %s (%s).",
		c.repo, c.short(), detail)}
}

func (c *contentCheck) contentMismatch(name string) *contentRefusal {
	return &contentRefusal{ErrorCode: "worktree_verify_content", hint: fmt.Sprintf(
		"Check whether a filter that rewrites %q on checkout gives different output each time it runs: that file in the new worktree didn't match commit %s. Less often, two committed names that differ only in letter case, or a damaged repository (run \"git fsck\" in %q to check), cause this.",
		name, c.short(), c.repo)}
}

// git runs one call of the check and returns its stdout. Each call gets a
// limit of its own: a stopped call does not stop the later calls of the check
// or of the create. On macOS a stopped `hash-object --stdin-paths` is
// followed by the compare step, and the create succeeds (measured).
func (c *contentCheck) git(dir string, stdin []byte, env []string, args ...string) ([]byte, string, error) {
	return c.gitAfterListing(false, dir, stdin, env, args...)
}

// gitAfterListing is git. With endAtDeadline set, a deadline that passed by the
// end of the config listing starts no call. The hash-object call of the files
// does not start then (4c111e86 and 87375cc7, measured on Linux, and 4c111e86 on
// Windows). That call is a helper of the deadline kill. After the kill the check
// does not wait for it on Windows (87375cc7, measured), and on Linux and macOS it
// waits at most fileHashGrace for its stdout (awaitFileHash).
func (c *contentCheck) gitAfterListing(endAtDeadline bool, dir string, stdin []byte, env []string, args ...string) ([]byte, string, error) {
	var track func(*os.Process)
	unreserve := func() {}
	s := c.u.stop
	if endAtDeadline && c.kills != nil && s != nil {
		kills := c.kills
		unreserve = kills.reserve()
		track = func(p *os.Process) { kills.trackOnce(p, unreserve) }
		s = &gitStop{wide: s.wide, beforeReap: track, beforeStart: kills.refuseStart}
	} else if s != nil {
		s = &gitStop{wide: s.wide}
	}
	ctx := s.context()
	cmd := c.cmd(ctx, dir, args...)
	if endAtDeadline && c.pastDeadline() {
		unreserve()
		return nil, "", errContentDeadline
	}
	cmd.Env = append(cmd.Env, env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if s == nil || track == nil {
		out, err := gitOutput(ctx, cmd)
		return out, errBuf.String(), err
	}
	ended := make(chan fileHashEnd, 1)
	go func() {
		out, err := gitOutput(ctx, cmd)
		unreserve()
		ended <- fileHashEnd{out, err}
	}()
	r, ok := awaitFileHash(ended, c.kills.firedCh, fileHashGrace())
	if !ok {
		return nil, "", errContentDeadline
	}
	return r.out, errBuf.String(), r.err
}

// cmd builds one call of the check in dir, after its config listing: b3081b51
// runs one before its `cat-file commit` (measured on Linux). A listing in the
// base repository gives the value of core.fileMode (noteFileMode).
func (c *contentCheck) cmd(ctx context.Context, dir string, args ...string) *exec.Cmd {
	u := c.u.atCommand()
	l := hookPrecursor(ctx, u, dir, false)
	if dir == c.repo {
		c.noteFileMode(l)
	}
	return hardenedGitCmd(ctx, u, dir, false, &configListing{hooks: l.hooks()}, args...)
}

// askRepo runs ask, when the check has one, and returns the text of a bad
// answer, or "".
func (c *contentCheck) askRepo() string {
	if c.ask == nil {
		return ""
	}
	text := c.ask()
	if text != "" {
		c.asked = true
	}
	return text
}

// failText is the detail of a failed call in the repository text. A stopped
// call gives the stop text (measured on Linux, macOS and Windows). Any other
// failure gives git's text (claustrum's own, not measured).
func failText(stderr string, err error) string {
	if isGitStop(err) {
		return err.Error()
	}
	return worktreeGitText(stderr, err)
}

// readObject reads one object of the commit (catFile) and hashes it again
// into the folder of the check. While the batch runs, the long-running
// hash-object of the kind hashes it. After that git stopped, a separate run
// reads the object again, and the batch runs on unused to the end of the
// check (c9125eb8, measured on Linux). text is the detail of a failure, or "".
func (c *contentCheck) readObject(kind, id string) (body []byte, text string) {
	body, stderr, err := c.catFile(kind, id)
	if err != nil {
		return nil, failText(stderr, err)
	}
	if c.batch != nil && !c.stopped {
		got, err := c.hashLong(kind, id, body)
		switch {
		case err == nil:
			if text := hashText(kind, id, got); text != "" {
				return nil, text
			}
			c.read++
			return body, ""
		case errors.Is(err, errContentDeadline):
			return nil, err.Error()
		case c.endedByKill(err): // also a hash-object that fails by itself after the kill: its stop line is not written
			return nil, errContentDeadline.Error()
		case !errors.Is(err, errNoHashHelper):
			logging.Infof(longStoppedLine, "hash-object", stopCause(err))
			c.stopped = true
			if body, stderr, err = c.catFile(kind, id); err != nil {
				return nil, failText(stderr, err)
			}
		}
	}
	if text := c.writeObject(kind, id, body); text != "" {
		return nil, text
	}
	c.read++
	return body, ""
}

// writeObject hashes body as an object of kind with a separate `hash-object
// -t <kind> --literally --no-filters --stdin -w` into the folder of the check.
// text is the detail of a failure, or "".
func (c *contentCheck) writeObject(kind, id string, body []byte) string {
	out, stderr, err := c.git(c.repo, body, objectWriteEnv(c.objects),
		"hash-object", "-t", kind, "--literally", "--no-filters", "--stdin", "-w")
	if err != nil {
		return failText(stderr, err)
	}
	return hashText(kind, id, strings.TrimSpace(string(out)))
}

// hashText is the detail of an object of kind whose hash got is not its id,
// or "". Not measured: the text is claustrum's own.
func hashText(kind, id, got string) string {
	if got != id {
		return "the stored " + kind + " does not match its id"
	}
	return ""
}

// treeOfCommit is the tree id of a commit object.
func treeOfCommit(body []byte) (string, bool) {
	line, _, _ := bytes.Cut(body, []byte("\n"))
	id, ok := bytes.CutPrefix(line, []byte("tree "))
	if !ok || !isObjectID(string(id)) {
		return "", false
	}
	return string(id), true
}

// treeWalk is one tree of the walk, with its files of the check and the trees
// below it, in the order of the tree.
type treeWalk struct {
	id, prefix string
	top        bool
	entries    []walkEntry
}

// walkEntry is one file of the check, or a tree below when sub is set.
type walkEntry struct {
	file contentFile
	sub  *treeWalk
}

// files is the files of the check in t and the trees below it, in the order of
// the trees.
func (t *treeWalk) files(out []contentFile) []contentFile {
	for _, e := range t.entries {
		if e.sub != nil {
			out = e.sub.files(out)
			continue
		}
		out = append(out, e.file)
	}
	return out
}

// walk reads the top tree id and the trees below it, one level of folder depth
// after the other (readLevel), and returns the files of the check in the order
// of the trees. In the top tree only the names of the check count. Below
// .claude every blob and link counts. A submodule entry is not compared
// (claustrum's own, not measured). Which object a refusal names when two are
// bad is claustrum's own, not measured. The order across levels is claustrum's
// own, not measured.
func (c *contentCheck) walk(id string) ([]contentFile, string) {
	top := &treeWalk{id: id, top: true}
	for level := []*treeWalk{top}; len(level) > 0; {
		bodies, text := c.readLevel(level)
		if text != "" {
			return nil, text
		}
		var next []*treeWalk
		for i, t := range level {
			if text := c.treeEntries(t, bodies[i]); text != "" {
				return nil, text
			}
			for _, e := range t.entries {
				if e.sub != nil {
					next = append(next, e.sub)
				}
			}
		}
		level = next
	}
	return top.files(nil), ""
}

// treeEntries reads the entries of the tree t from its body.
func (c *contentCheck) treeEntries(t *treeWalk, body []byte) string {
	id, prefix, top := t.id, t.prefix, t.top
	idLen := len(c.commit) / 2
	for len(body) > 0 {
		sp := bytes.IndexByte(body, ' ')
		nul := bytes.IndexByte(body, 0)
		if sp < 0 || nul < sp || len(body) < nul+1+idLen {
			return "the stored tree " + id + " cannot be read" // not measured
		}
		mode, name := string(body[:sp]), string(body[sp+1:nul])
		entry := fmt.Sprintf("%x", body[nul+1:nul+1+idLen])
		body = body[nul+1+idLen:]
		if top {
			c.topNames = append(c.topNames, name)
		}
		isTree := mode == "40000"
		isFile := mode == "100644" || mode == "100755" || mode == "120000"
		if top && !topContentName(name) {
			continue
		}
		switch {
		case isTree && (!top || strings.EqualFold(name, ".claude")):
			t.entries = append(t.entries, walkEntry{sub: &treeWalk{id: entry, prefix: prefix + name + "/"}})
		case isFile:
			t.entries = append(t.entries, walkEntry{file: contentFile{path: prefix + name, mode: mode, id: entry}})
		}
	}
	return ""
}

// topContentName reports whether name at the top of the commit is a name of
// the check. .claude matches without regard to case: on macOS and Windows
// b3081b51 names `.Claude/a` beside `.claude/a` in its text (measured).
func topContentName(name string) bool {
	if strings.EqualFold(name, ".claude") || strings.EqualFold(name, ".mcp.json") {
		return true
	}
	return slices.Contains(contentTopNames[1:], name)
}

// leadsToTop reports whether the link at path in leaf leads to leaf itself.
func leadsToTop(leaf, path string) bool {
	at, err := os.Stat(filepath.Join(leaf, filepath.FromSlash(path)))
	if err != nil {
		return false
	}
	top, err := os.Stat(leaf)
	return err == nil && os.SameFile(at, top)
}

// dropSkippedFiles leaves out each file that is missing in the new worktree and
// that the index of the new worktree marks skip-worktree. A sparse checkout did
// not write it. With core.sparseCheckout and a sparse-checkout file of only
// comment lines, 4c111e86 checks no file under .claude and the create succeeds
// (measured on Linux). macOS, Windows and cone mode are claustrum's own, not
// measured. So is the call: one `ls-files -t -z` in the new worktree, with the
// top names of the missing files, only when a file is missing. A failed call
// drops no file. A missing file with no skip-worktree mark still refuses.
func (c *contentCheck) dropSkippedFiles(files []contentFile) []contentFile {
	missing := map[string]bool{}
	var tops []string
	for _, f := range files {
		if _, err := os.Lstat(filepath.Join(c.leaf, filepath.FromSlash(f.path))); !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		missing[f.path] = true
		if top, _, _ := strings.Cut(f.path, "/"); !slices.Contains(tops, top) {
			tops = append(tops, top)
		}
	}
	if len(tops) == 0 {
		return files
	}
	out, _, err := c.git(c.leaf, nil, nil, append([]string{"--literal-pathspecs", "ls-files", "-t", "-z", "--"}, tops...)...)
	if err != nil {
		return files
	}
	skipped := map[string]bool{}
	for rec := range strings.SplitSeq(string(out), "\x00") {
		if p, ok := strings.CutPrefix(rec, "S "); ok {
			skipped[p] = true
		}
	}
	kept := make([]contentFile, 0, len(files))
	for _, f := range files {
		if !missing[f.path] || !skipped[f.path] {
			kept = append(kept, f)
		}
	}
	return kept
}

// worktreeIDs is the ids of the files in the new worktree from one
// `hash-object --no-filters --stdin-paths` call in the base repository
// (measured on Linux), or nil when that call fails or a path holds a line end.
// failed reports a call that failed and that no limit stopped.
func (c *contentCheck) worktreeIDs(files []contentFile) (ids []string, failed bool) {
	if len(files) == 0 {
		return nil, false
	}
	var in strings.Builder
	for _, f := range files {
		p := filepath.Join(c.leaf, filepath.FromSlash(f.path))
		if strings.ContainsAny(p, "\r\n") {
			return nil, false
		}
		in.WriteString(p + "\n")
	}
	out, _, err := c.gitAfterListing(true, c.repo, []byte(in.String()), nil, "hash-object", "--no-filters", "--stdin-paths")
	if err != nil {
		return nil, !isGitStop(err)
	}
	ids = strings.Fields(string(out))
	if len(ids) != len(files) {
		return nil, false
	}
	return ids, false
}

// changedWhileChecked tests each file before the hash-object call and after a
// failed one. On Windows a file that another process holds open for writing
// with no share mode gives the retry code before that call (measured on Windows). A file
// that is no longer of the kind of the commit gives the other_writer code: a
// regular file that a link to "." replaced during that call does (measured on
// Linux). A file that cannot be read gives the retry code, and so does a file
// that is gone, with "no such file or directory" (measured on Linux). With no such
// file the check goes on to the compare (claustrum's own, not measured). On
// Linux a link in place of a file gives the retry code first if a root at the
// new worktree cannot follow it. A loop then gives "too many levels of symbolic
// links" (b3081b51, measured on Linux). macOS and Windows keep the other_writer
// code there (claustrum's own, not measured).
func (c *contentCheck) changedWhileChecked(files []contentFile) *contentRefusal {
	for _, f := range files {
		p := filepath.Join(c.leaf, filepath.FromSlash(f.path))
		fi, err := os.Lstat(p)
		if err == nil {
			link := fi.Mode()&fs.ModeSymlink != 0
			if link {
				err = leafLinkErr(c.leaf, f.path)
			}
			switch {
			case err != nil:
				// The link cannot be followed: the retry code below.
			case link != (f.mode == "120000") || (!link && !fi.Mode().IsRegular()):
				err = errChangedKind
			case !link:
				var fh *os.File
				if fh, err = os.Open(p); err == nil {
					_ = fh.Close()
				}
			}
		}
		switch {
		case err == nil:
		case errors.Is(err, errChangedKind):
			return &contentRefusal{ErrorCode: "worktree_verify_other_writer", hint: fmt.Sprintf(
				"Check what else writes into the folder the worktree is made in, or run \"git fsck\" in %q: %q in the new worktree isn't what commit %s has there (it changed while it was being checked).",
				c.repo, f.path, c.short())}
		default:
			return c.retryRefusal(f, err)
		}
	}
	return nil
}

// lostExecBit refuses a file that the commit records with mode 100755 and
// that has no execute bit in the new worktree (b3081b51, measured on Linux and
// macOS). The mode is read before the hash-object call, so a change during
// that call passes (measured on Linux). With core.fileMode false in any config
// scope that git reads in the base repository the file passes (b3081b51,
// measured on Linux and macOS, for the repository config). The read of that
// setting is noteFileMode.
// Windows has no such test (claustrum's own, not measured). The place of this
// test after the other tests before that call is claustrum's own (not
// measured).
func (c *contentCheck) lostExecBit(files []contentFile) *contentRefusal {
	for _, f := range files {
		if f.mode != "100755" {
			continue
		}
		fi, err := os.Lstat(filepath.Join(c.leaf, filepath.FromSlash(f.path)))
		if err != nil || !fi.Mode().IsRegular() || !lacksExecBit(fi) || c.fileModeOff {
			continue
		}
		return &contentRefusal{ErrorCode: "worktree_verify_content", hint: fmt.Sprintf(
			"Run \"git config core.fileMode false\" in %q if this file system doesn't keep executable bits, otherwise \"git fsck\" there: %q came out not executable though commit %s records it executable.",
			c.repo, f.path, c.short())}
	}
	return nil
}

// noteFileMode reads core.fileMode from the listing l before a call of the
// check in the base repository. c9125eb8 makes no config call of its own for
// it (measured on Linux, macOS and Windows). Where it reads the value is not
// measured, so the listing is claustrum's own source. The listing shows every
// config scope that git reads there, and its last core.filemode counts. A
// listing that failed keeps the value before it. The words of readBoolWord
// give the value, and a word that it does not take keeps the execute-bit test
// (claustrum's own, not measured).
func (c *contentCheck) noteFileMode(l listingRun) {
	if l.err != nil {
		return
	}
	off := false
	for rec := range strings.SplitSeq(string(l.out), "\x00") {
		key, value, has := strings.Cut(rec, "\n")
		if key == "core.filemode" {
			v, ok := readBoolWord(repoSetting{key: key, value: value, hasValue: has})
			off = ok && v == "false"
		}
	}
	c.fileModeOff = off
}

// linkFault is what the chain of a committed link runs into.
type linkFault int

const (
	linkNoFault linkFault = iota
	// linkTooLong is a circle, or a chain of more than contentLinkLimit
	// links inside the new worktree.
	linkTooLong
	// linkThroughProc is a chain that passes through /proc or /dev/fd.
	linkThroughProc
)

// contentLinkLimit is the count of links inside the new worktree that the
// check follows in a chain: 8 pass and 9 are refused (b3081b51, measured on
// macOS). The 8 is in the text on Linux and macOS too.
const contentLinkLimit = 8

// errChangedKind marks a file that is no longer of the kind of the commit.
var errChangedKind = errors.New("the file changed its kind")

// retryRefusal is the retry code for the file f that cannot be read.
func (c *contentCheck) retryRefusal(f contentFile, err error) *contentRefusal {
	cause := err
	var pe *fs.PathError
	if errors.As(err, &pe) {
		cause = pe.Err
	}
	return &contentRefusal{ErrorCode: "worktree_verify_retry", hint: fmt.Sprintf(
		"Try again, and if this repeats check the permissions of the folder the worktree is made in: %q in the new worktree couldn't be read to check it against commit %s (%s).",
		f.path, c.short(), cause.Error())}
}

// compare tests one file whose id did not match. The file of the new worktree
// is compared with the blob, and then with the blob through the filters of its
// path. A file that cannot be read gives the retry code with the error of the
// system: "permission denied" on Linux, "Access is denied." on Windows
// (measured).
func (c *contentCheck) compare(f contentFile) *contentRefusal {
	p := filepath.Join(c.leaf, filepath.FromSlash(f.path))
	have, link, err := worktreeFileBytes(p)
	switch {
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, errNotAFile):
		// A file that is gone or is no file is not measured (claustrum's own).
		return c.contentMismatch(f.path)
	case err != nil:
		return c.retryRefusal(f, err)
	}
	// On Linux a link in place of a committed file that leaves the new worktree
	// gives the retry code, before the blob is read. Its cause is "path escapes
	// from parent" (b3081b51, measured on Linux). macOS and Windows keep the
	// content code (claustrum's own, not measured).
	if link && f.mode != "120000" {
		if err := leafLinkErr(c.leaf, f.path); err != nil {
			return c.retryRefusal(f, err)
		}
	}
	blob, ok := c.linkBlobs[f.path]
	if !ok {
		var stderr string
		blob, stderr, err = c.git(c.repo, nil, nil, "cat-file", "blob", f.id)
		if err != nil {
			return c.repositoryRefusal(failText(stderr, err))
		}
		// A failed or wrong hash of the blob refuses with the repository code
		// (claustrum's own, not measured).
		if text := c.writeObject("blob", f.id, blob); text != "" {
			return c.repositoryRefusal(text)
		}
		c.read++
	}
	if bytes.Equal(have, blob) {
		return nil
	}
	if link || f.mode == "120000" {
		return c.contentMismatch(f.path)
	}
	// A Git LFS pointer blob whose path has the filter lfs gets no filter call.
	// The file passes when its sha256 is the oid of the pointer. 4c111e86 runs
	// one smudge for a good LFS file under .claude, and it refuses wrong bytes of
	// the same size with the LFS text (measured on macOS). From the code: a
	// filter call here runs git-lfs with the store of the settings copy, where
	// the object is absent. Linux, Windows and the attribute test are
	// claustrum's own, not measured.
	if p, ok := readPointerBlob(blob); ok && c.lfsFiltered(f.path) {
		if lfsContentMatches(have, p) {
			return nil
		}
		return c.lfsPointerMismatch(f.path)
	}
	// A stopped filter call gives the content code (measured on
	// Windows). The call has no `-c` pairs of the filter rule (claustrum's
	// own, not measured).
	filtered, _, err := c.git(c.leaf, nil, nil, "cat-file", "--filters", "--path="+f.path, f.id)
	if err != nil || !bytes.Equal(have, filtered) {
		return c.contentMismatch(f.path)
	}
	return nil
}

// lfsFiltered reports whether the filter attribute of path is lfs in the new
// worktree. The first ask runs one `check-attr -z --stdin filter` there for
// every file of the compare step that is no link. A failed call counts no
// path, and the filter call runs as before (claustrum's own, not measured).
func (c *contentCheck) lfsFiltered(path string) bool {
	if c.lfsFiles == nil {
		c.lfsFiles = map[string]bool{}
		var in strings.Builder
		for _, f := range c.differ {
			if f.mode != "120000" {
				in.WriteString(f.path + "\x00")
			}
		}
		out, _, err := c.git(c.leaf, []byte(in.String()), nil, "check-attr", "-z", "--stdin", "filter")
		if err == nil {
			// Each record is <path> NUL filter NUL <value> NUL.
			recs := strings.Split(string(out), "\x00")
			for i := 0; i+2 < len(recs); i += 3 {
				if recs[i+1] == "filter" && recs[i+2] == "lfs" {
					c.lfsFiles[recs[i]] = true
				}
			}
		}
	}
	return c.lfsFiles[path]
}

// lfsContentMatches reports whether the sha256 of have is the oid of the pointer
// p. The size line is not tested (claustrum's own, not measured).
func lfsContentMatches(have []byte, p pointerRef) bool {
	sum := sha256.Sum256(have)
	return hex.EncodeToString(sum[:]) == p.oid
}

// lfsPointerMismatch is the refusal of a Git LFS file whose bytes are not the
// object of its pointer (4c111e86, measured on macOS).
func (c *contentCheck) lfsPointerMismatch(name string) *contentRefusal {
	return &contentRefusal{ErrorCode: "worktree_verify_content", hint: fmt.Sprintf(
		"Turn off Worktree for this session, then try again: the Git LFS content checked out at %q doesn't match its pointer in commit %s, so a Git LFS store on the remote machine holds wrong bytes for it, and each new worktree would get the same bytes.",
		name, c.short())}
}

var errNotAFile = errors.New("not a file")

// worktreeFileBytes is the content of the file at p as git hashes it: the
// target of a link, or the bytes of a regular file.
func worktreeFileBytes(p string) (b []byte, link bool, err error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, false, err
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		t, err := os.Readlink(p)
		return []byte(filepath.ToSlash(t)), true, err
	case fi.Mode().IsRegular():
		b, err := os.ReadFile(p)
		return b, false, err
	}
	return nil, false, errNotAFile
}

// otherWriter refuses a name of the check at the top of the new worktree that
// the commit does not have (measured on Linux, macOS and Windows, for
// AGENTS.md). The names come from a read of the folder, so another case of a
// committed name does not count. A new file below .claude is not looked at
// (claustrum's own, not measured).
func (c *contentCheck) otherWriter() *contentRefusal {
	ents, err := os.ReadDir(c.leaf)
	if err != nil {
		return nil
	}
	for _, e := range ents {
		name := e.Name()
		if contentNameAtTop(name) && !slices.Contains(c.topNames, name) {
			return &contentRefusal{ErrorCode: "worktree_verify_other_writer", hint: fmt.Sprintf(
				"Check what else writes into the folder the worktree is made in, or run \"git fsck\" in %q: %q appeared in the new worktree though commit %s has no such file.",
				c.repo, name, c.short())}
		}
	}
	return nil
}

// contentNameAtTop reports whether name is one of contentTopNames exactly.
func contentNameAtTop(name string) bool {
	return slices.Contains(contentTopNames, name)
}

// contentUndoText is the text of a failed rollback after a refusal of the
// check. It starts " Removing it could not finish for <leaf>: " and ends with
// a period, where the other frames start "; and the undo could not finish for
// <leaf>: " (measured on Windows, for a leaf entry that cannot be removed).
// The same form for the other texts of the rollback is claustrum's own (not
// measured).
func contentUndoText(undo string) string {
	rest, ok := strings.CutPrefix(undo, "; and the undo could not finish for ")
	if !ok {
		return undo
	}
	return " Removing it could not finish for " + rest + "."
}
