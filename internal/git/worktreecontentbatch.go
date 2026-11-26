package git

import (
	"bufio"
	"bytes"
	"crypto/sha1" //nolint:gosec // a probe of the fips140 mode, no hash of data
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/schubydoo/claustrum/internal/homeguard"
	"github.com/schubydoo/claustrum/internal/logging"
)

// The object reads of the content check. c9125eb8 reads the commit and the
// trees through one long-running `git cat-file --batch` per create (measured
// on Linux, macOS and Windows), and the checked line counts 0 separate runs on
// Windows too. A git that stops answering, or a process
// that cannot compute SHA-1, ends that use. Then one `cat-file <kind> <id>`
// reads each object above the level below the top tree, with the same result (measured on Linux). The checked
// line counts the commits and trees that separate runs read.
//
// While the batch runs, c9125eb8 hashes the commits with one long-running
// `hash-object -t commit --literally --no-filters --stdin-paths -w` and the
// trees with one such call of `-t tree` (measured on Linux, macOS and
// Windows). Each starts at the first object of its kind and ends with the
// batch (measured on Linux). After a hash-object that stopped answering, the
// batch reads no more objects, and separate runs read and hash that object and
// the rest: `cat-file <kind> <id>`, then `hash-object -t <kind> --literally
// --no-filters --stdin -w`. The batch process runs on to the end of the check
// (c9125eb8, measured on Linux).
//
// Claustrum's own, not measured: the blobs of committed links go through the
// batch too and do not count. The blob of the compare step is always a
// separate run. An object that the batch names missing, ambiguous or of
// another kind is read again by a separate run, and the batch goes on. A
// malformed answer, a failed start and a stop at the limit end the batch as an
// end of its output does. The batch and each hash-object have the limit of one
// call for the whole check.
//
// The long-running hash-object reads each object from a file named by its id
// in a folder `.readback-scratch-<n>` beside the folder of the objects. The
// folder is made at the first such object, after its git started. Each file
// goes right after the answer for it, so the folder holds at most one file.
// The folder goes at the end of the check (4c111e86, measured on Linux and
// macOS. Windows shows the folder with a file named by an object id during a
// check). The folder has mode 0700 and a file mode 0600. The modes and the
// start order are of 4c111e86, measured on Linux.
//
// After a stop c9125eb8 reads the trees of a level below the top with one
// `cat-file --buffer --batch` and hashes them with a new long-running
// hash-object of the trees (measured on Linux, readLevel). The top tree is read
// and hashed alone. After a fault of that read or of that hash-object, the
// levels below it are read by separate runs (measured on Linux).

// longStoppedLine is the log line after a long-running git of the check
// stopped. Its first part is the git command, cat-file or hash-object, and the
// second is the cause (stopCause). The texts are of c9125eb8 (measured on
// Linux).
const longStoppedLine = "[GitHandler] worktree_create: stopped using the long-running git %s for the new worktree's config check: %s; the rest is read with a git process per object or per folder depth (slower, same result)"

// stopCause is the cause of longStoppedLine. The batch header "xyz blob" with
// no size gives the first text (c9125eb8, measured on Linux).
// The same text for the other answers that do not parse is claustrum's own.
// An end of the output gives "it stopped answering (EOF)" for the batch and
// for hash-object (measured on Linux). A git that ends after its first answer
// gives the broken pipe of the next write (answered): 4c111e86 logs it for the
// batch (measured on Linux), and the hash-object case is claustrum's own.
func stopCause(err error) string {
	if errors.Is(err, errBatchAnswer) {
		return "it answered with something other than the object asked for"
	}
	return fmt.Sprintf("it stopped answering (%v)", err)
}

// noSHA1Line is the log line of a check that starts no batch, because this
// process cannot compute SHA-1 (c9125eb8, measured on Linux).
const noSHA1Line = "[GitHandler] worktree_create: this process cannot compute SHA-1 (GODEBUG=fips140=only), so the new worktree's config check reads each commit, folder and link with separate git processes instead of the long-running ones (slower, same result)"

// sha1Works reports whether this process can compute SHA-1. With
// GODEBUG=fips140=only the Write of crypto/sha1 fails (from the code of Go).
// Tests replace it.
var sha1Works = func() bool {
	_, err := sha1.New().Write([]byte{0}) //nolint:gosec // see the import
	return err == nil
}

// errBatchAnswer is the cause of an answer of the batch that does not parse.
var errBatchAnswer = errors.New("an answer that cannot be read")

// errNoHashHelper marks an object that the long-running hash-object does not
// take: its file cannot be written, its path holds a line end, or no pipe
// opened. A separate run hashes that object, and the long-running gits go on
// (claustrum's own, not measured).
var errNoHashHelper = errors.New("no long-running hash-object for this object")

// objectBatch is one long-running git of the check: `cat-file --batch`, or a
// hash-object with --stdin-paths.
//
// The daemon keeps its copy of the read end of the stdin pipe until the first
// answer of git, which shows that git started (answered). So a write to a git
// that ended after an answer fails with "write |1: broken pipe". 4c111e86 logs
// that cause when the batch ends after its first answer, and an end of the
// output for a batch that ends before its first answer (measured on Linux).
type objectBatch struct {
	in   *os.File // our end of the stdin of git
	inR  *os.File // the end of git, closed once at the first answer or after the drop and the end of git (closeInR)
	outR *io.PipeReader
	out  *bufio.Reader
	done chan struct{}
	// closeInR closes inR once. It is nil when the daemon holds no inR.
	closeInR func()
	// mu guards ended, the end of git, and dropped, the drop of the batch.
	mu      sync.Mutex
	ended   bool
	dropped bool
}

// answered closes the copy of the daemon of the read end of the stdin pipe, at
// the first answer of git.
func (b *objectBatch) answered() {
	if b.closeInR != nil {
		b.closeInR()
	}
}

// startBatch starts the batch of the check in the base repository, with the
// config listing before it, as each call of the check. A failed start shows
// at the first read as an end of the output.
//
// Before the batch git is asked about the repository (askRepo). After a bad
// answer no batch starts, the line longStoppedLine says "it did not start",
// and the next separate run asks again (c9125eb8, measured on macOS). The
// read per folder depth after that line is claustrum's own, not measured.
func (c *contentCheck) startBatch() {
	if !sha1Works() {
		logging.Infof("%s", noSHA1Line)
		return
	}
	if c.askRepo() != "" {
		logging.Infof(longStoppedLine, "cat-file", "it did not start")
		c.stopped = true
		return
	}
	// No pipe: every object is read by a separate run, with no line
	// (claustrum's own, not measured).
	c.batch = c.startLong(false, true, nil, "cat-file", "--batch")
}

// startLong starts one long-running git of the check in the base repository,
// with env after its environment. It gets the limit of one call, and its stop
// stops no other call (contentCheck.git). It is nil when no pipe opened. With
// endAtDeadline set, it is also nil when the deadline passed by the end of the
// config listing, and no git starts (gitAfterListing). With killed set, the deadline
// kills this git (helperKills), and a start under way counts at the end of the check.
func (c *contentCheck) startLong(endAtDeadline, killed bool, env []string, args ...string) *objectBatch {
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil
	}
	s := c.u.stop
	if s != nil {
		s = &gitStop{wide: s.wide}
	}
	kills := c.kills
	unreserve := func() {}
	if killed && kills != nil && s != nil {
		unreserve = kills.reserve()
	} else {
		kills = nil
	}
	ctx := s.context()
	cmd := c.cmd(ctx, c.repo, args...)
	if endAtDeadline && c.pastDeadline() {
		unreserve()
		_ = inR.Close()
		_ = inW.Close()
		return nil
	}
	cmd.Env = append(cmd.Env, env...)
	outR, outW := io.Pipe()
	cmd.Stdin, cmd.Stdout = inR, outW
	b := &objectBatch{in: inW, inR: inR, outR: outR, out: bufio.NewReader(outR), done: make(chan struct{}),
		closeInR: sync.OnceFunc(func() { _ = inR.Close() })}
	if kills != nil {
		s.beforeReap = func(p *os.Process) {
			kills.runTracked(&deadlineHelper{p: p, drop: b.drop, exited: make(chan struct{})}, unreserve)
		}
		if endAtDeadline {
			s.beforeStart = kills.refuseStart
		}
	}
	go func() {
		defer close(b.done)
		_ = gitRun(ctx, cmd)
		unreserve()
		// Any end of git reads as an end of its output (io.EOF).
		_ = outW.Close()
		b.endedOrDropped(&b.ended)
	}()
	return b
}

// read asks the batch for the object id. ok is false for an object that the
// batch names missing or that is not of kind. err is an end or a fault of the
// batch, after which it gives no answer.
func (b *objectBatch) read(kind, id string) (body []byte, ok bool, err error) {
	if _, err := io.WriteString(b.in, id+"\n"); err != nil {
		return nil, false, err
	}
	return b.answer(kind, id)
}

// answer reads the answer of the batch for the object id that it was asked
// for, as read does.
func (b *objectBatch) answer(kind, id string) (body []byte, ok bool, err error) {
	line, err := b.out.ReadString('\n')
	if err != nil {
		return nil, false, err
	}
	b.answered()
	f := strings.Fields(line)
	if len(f) == 2 && f[0] == id && (f[1] == "missing" || f[1] == "ambiguous") {
		return nil, false, nil
	}
	if len(f) != 3 || f[0] != id {
		return nil, false, errBatchAnswer
	}
	size, err := strconv.ParseInt(f[2], 10, 64)
	if err != nil || size < 0 {
		return nil, false, errBatchAnswer
	}
	// The body grows as it comes, so a size in a false header reserves no
	// memory. No size is refused (claustrum's own, not measured).
	var buf bytes.Buffer
	if _, err := io.CopyN(&buf, b.out, size); err != nil {
		return nil, false, err
	}
	if end, err := b.out.ReadByte(); err != nil {
		return nil, false, err
	} else if end != '\n' {
		return nil, false, errBatchAnswer
	}
	if f[1] != kind {
		return nil, false, nil
	}
	return buf.Bytes(), true, nil
}

// close ends the batch: git reads the end of its stdin and exits. The wait
// ends with git, at the latest at the limit of the call.
func (b *objectBatch) close() {
	b.drop()
	<-b.done
}

// drop closes the pipes of the batch and does not wait for git. A read that
// waits for an answer then ends with an error. git ends at the end of its
// stdin, or at the limit of the call (claustrum's own). After that end the
// copy of the daemon of the read end of the stdin pipe closes (endedOrDropped).
func (b *objectBatch) drop() {
	_ = b.in.Close()
	_ = b.outR.Close()
	b.endedOrDropped(&b.dropped)
}

// endedOrDropped sets flag, ended or dropped. Once both are set, the copy of
// the daemon of the read end of the stdin pipe closes. Before the end of git
// the start of git can still read it. Before the drop a write can still come,
// which gives an end of the output, not a broken pipe, for a git that ended
// before its first answer.
func (b *objectBatch) endedOrDropped(flag *bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	*flag = true
	if b.ended && b.dropped {
		b.answered()
	}
}

// hash asks a long-running hash-object for the id of the file at path. err
// is an end of the git, after which it gives no answer.
func (b *objectBatch) hash(path string) (string, error) {
	if _, err := io.WriteString(b.in, path+"\n"); err != nil {
		return "", err
	}
	line, err := b.out.ReadString('\n')
	if err != nil {
		return "", err
	}
	b.answered()
	return strings.TrimSpace(line), nil
}

// stopBatch ends the batch and the long-running hash-object calls of the
// check, if they run. After the deadline it does not wait for them.
func (c *contentCheck) stopBatch() {
	end := c.endLong
	if c.atDeadline {
		// The kill comes before the close of stdin (measured on Linux).
		c.kills.fire()
		end = (*objectBatch).drop
	}
	if c.batch != nil {
		end(c.batch)
		c.batch = nil
	}
	for kind, h := range c.hashers {
		end(h)
		delete(c.hashers, kind)
	}
}

// hashLong hashes body, the object id of kind, with the long-running
// hash-object of that kind, which it starts at the first object of the kind.
func (c *contentCheck) hashLong(kind, id string, body []byte) (string, error) {
	got, h, err := c.hashOn(c.hashers[kind], kind, id, body)
	if h != nil && c.hashers[kind] == nil {
		if c.hashers == nil {
			c.hashers = map[string]*objectBatch{}
		}
		c.hashers[kind] = h
	}
	return got, err
}

// hashOn hashes body, the object id of kind, with h, a long-running
// hash-object of that kind. A nil h starts one. h is the one that ran, or nil.
// The object goes to the file id in the scratch folder, which goes after the
// answer (removeObjectBody). The git starts before the folder and the file
// (4c111e86, measured on Linux). An id that is no object id takes no file,
// and a git started for an object with no file stays for the next one
// (claustrum's own).
func (c *contentCheck) hashOn(h *objectBatch, kind, id string, body []byte) (string, *objectBatch, error) {
	if !isObjectID(id) {
		return "", h, errNoHashHelper
	}
	if h == nil {
		// A deadline that passed by the end of its listing starts no hash-object,
		// and the object does not count (4c111e86 and 87375cc7, measured on Linux).
		h = c.startLong(true, true, objectWriteEnv(c.objects), "hash-object", "-t", kind, "--literally", "--no-filters", "--stdin-paths", "-w")
		if h == nil {
			if c.atDeadline {
				return "", nil, errContentDeadline
			}
			return "", nil, errNoHashHelper
		}
	}
	root, err := c.scratchDir()
	if err != nil {
		return "", h, errNoHashHelper
	}
	p := filepath.Join(root.Name(), id)
	if strings.ContainsAny(p, "\r\n") {
		return "", h, errNoHashHelper
	}
	if err := writeObjectBody(root, id, body); err != nil {
		// A part of the file that the write left goes too, so the folder
		// holds no file after a failed write (claustrum's own).
		_ = removeObjectBody(root, id)
		return "", h, errNoHashHelper
	}
	defer func() { _ = removeObjectBody(root, id) }()
	got, err := h.hash(p)
	return got, h, err
}

// scratchDir returns the root of the scratch folder of the check, which it
// makes and opens at the first call (requestDir, openRootNoWait). Every write
// and remove of a file goes through that one root, so a link put at the path
// of the folder after the open is never followed. The remove of requestDir goes in
// dropScratch. A folder that does not open goes at once.
func (c *contentCheck) scratchDir() (*os.Root, error) {
	if c.scratch != nil {
		return c.scratch, nil
	}
	dir, remove, err := c.u.requestDir(shadowScratchDirPrefix, scratchTempPrefix)
	if err != nil {
		return nil, err
	}
	root, err := openScratchRoot(dir)
	if err != nil {
		remove()
		return nil, err
	}
	// The root must be the folder that the mkdir made. An entry put at its
	// path between the mkdir and the open stays, and the check hashes with no
	// file (claustrum's own).
	made, lerr := os.Lstat(dir)
	opened, serr := root.Stat(".")
	if lerr != nil || serr != nil || !made.IsDir() || !os.SameFile(made, opened) {
		_ = root.Close()
		return nil, errors.New("the scratch folder changed after its mkdir")
	}
	c.scratch, c.dropScratch = root, remove
	return root, nil
}

// openScratchRoot opens the scratch folder that scratchDir just made. Tests
// replace it, to change the folder between its mkdir and its open.
var openScratchRoot = openRootNoWait

// removeScratch closes the root of the scratch folder and removes the folder,
// if it was made.
func (c *contentCheck) removeScratch() {
	if c.scratch != nil {
		_ = c.scratch.Close()
	}
	if c.dropScratch != nil {
		c.dropScratch()
	}
	c.scratch, c.dropScratch = nil, nil
}

// writeObjectBody writes body to the file name through root, the scratch
// folder of the check. Tests replace it.
var writeObjectBody = func(root *os.Root, name string, body []byte) error {
	return root.WriteFile(name, body, 0o600)
}

// removeObjectBody removes name, the file of one object that writeObjectBody
// wrote through root, the scratch folder of the check. After
// homeguard.WipesHomeDir it is one plain remove of a regular file by name
// through that root, never a tree and never a link target. An entry put in
// its place between the test of its kind and the remove goes by that one
// remove too. The root, as for the write, also reaches a file under a folder
// of the daemon of mode 0300, which 4c111e86 empties there (measured on
// Linux). It is a variable so that a test can see the call.
var removeObjectBody = func(root *os.Root, name string) error {
	if homeguard.WipesHomeDir(filepath.Join(root.Name(), name)) {
		return errors.New("refusing to delete a path that holds the home folder")
	}
	fi, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return errors.New("the object file is not a regular file")
	}
	return root.Remove(name)
}

// objectWriteEnv is the environment after the environment of a hash-object
// call with -w: its objects go to dir, with no alternates (b3081b51 and
// c9125eb8, measured on Linux). GIT_FLUSH=1 comes last (c9125eb8, measured on
// Linux, macOS and Windows; b3081b51 sets none).
func objectWriteEnv(dir string) []string {
	return []string{"GIT_OBJECT_DIRECTORY=" + dir, "GIT_ALTERNATE_OBJECT_DIRECTORIES=", "GIT_FLUSH=1"}
}

// catFile reads the object id of kind: through the batch while it answers,
// and by a separate `cat-file <kind> <id>` run otherwise. A commit or a tree
// that a separate run read counts in c.separate. A bad answer of git about the
// repository before a separate run (askRepo) is the error, with its text, and
// the run does not start (c9125eb8, measured on macOS for the commit).
func (c *contentCheck) catFile(kind, id string) ([]byte, string, error) {
	if c.pastDeadline() {
		return nil, "", errContentDeadline
	}
	if c.batch != nil && !c.stopped {
		body, ok, err := c.readAtDeadline(c.batch, kind, id)
		switch {
		case c.pastDeadline():
			return nil, "", errContentDeadline
		case err != nil:
			logging.Infof(longStoppedLine, "cat-file", stopCause(err))
			c.stopBatch()
			c.stopped = true
		case ok:
			return body, "", nil
		}
	}
	if text := c.askRepo(); text != "" {
		return nil, "", errors.New(text)
	}
	body, stderr, err := c.git(c.repo, nil, nil, "cat-file", kind, id)
	if err == nil && kind != "blob" {
		c.separate++
	}
	return body, stderr, err
}

// readLevel reads and hashes the trees of one level of folder depth, and
// returns their bodies in the order of level. Each tree goes through
// readObject, except after a stop of a long-running git below the top tree:
// then one `cat-file --buffer --batch` reads the trees of the level, and a new
// long-running `hash-object -t tree --literally --no-filters --stdin-paths -w`
// hashes them (c9125eb8, measured on Linux). A tree that the read gave no body
// for, and each tree after a stop of that hash-object, go through readObject
// as a separate run. That stop logs no line (measured on Linux).
//
// A fault ends the reads per folder depth for the rest of the check
// (depthEnded). It is an answer of the read that does not parse or does not
// come, or a stop of its tree hash-object. c9125eb8 makes one `cat-file
// --buffer --batch` in a tree of three levels below the top then, and separate
// runs read the other levels (measured on Linux, for both faults).
//
// A read of a level with one tree counts as one separate run (4c111e86,
// measured on Linux). A batch that answers the commit and then ends gives 4:
// the top tree by its own call and three such reads.
//
// Claustrum's own, not measured: each tree of that read counts in
// c.separate, the trees of one level go to the read in the
// order of the walk, the hash-object of the level ends with the level, no second one starts in a level
// after a stop, and a tree whose body cannot be written for it is hashed by a
// separate run. With no fault each level gets its own read.
func (c *contentCheck) readLevel(level []*treeWalk) ([][]byte, string) {
	read := make([][]byte, len(level))
	if c.pastDeadline() {
		return nil, errContentDeadline.Error()
	}
	if c.stopped && !c.depthEnded && !level[0].top {
		var faulted bool
		read, faulted = c.readByDepth(level)
		c.depthEnded = faulted
	}
	bodies := make([][]byte, len(level))
	var h *objectBatch
	defer func() {
		if h != nil {
			c.endLong(h)
		}
	}()
	hashing := true
	for i, t := range level {
		if body := read[i]; body != nil && hashing {
			got, ran, err := c.hashOn(h, "tree", t.id, body)
			h = ran
			switch {
			case err == nil:
				if text := hashText("tree", t.id, got); text != "" {
					return nil, text
				}
				c.read++
				c.separate++
				bodies[i] = body
				continue
			case errors.Is(err, errContentDeadline):
				return nil, err.Error()
			case errors.Is(err, errNoHashHelper):
				if text, _ := c.writeObject("tree", t.id, body); text != "" {
					return nil, text
				}
				c.read++
				c.separate++
				bodies[i] = body
				continue
			}
			// The stopped git ends here, so a later tree of the level would
			// start a new one without the flag.
			c.endLong(h)
			h = nil
			hashing = false
			c.depthEnded = true
		}
		body, text, _ := c.readObject("tree", t.id)
		if text != "" {
			return nil, text
		}
		bodies[i] = body
	}
	return bodies, ""
}

// readByDepth reads the trees of level with one `cat-file --buffer --batch`.
// The ids go to its stdin, which then closes. The answers are read while the
// ids go in, so a full pipe holds neither side. A body is nil for a tree that
// the batch named missing or of another kind, and for each tree from the first
// answer that does not parse or does not come. faulted is true after such an
// answer. No pipe gives no fault (claustrum's own, not measured).
func (c *contentCheck) readByDepth(level []*treeWalk) (read [][]byte, faulted bool) {
	read = make([][]byte, len(level))
	b := c.startLong(false, false, nil, "cat-file", "--buffer", "--batch")
	if b == nil {
		return read, false
	}
	var ids strings.Builder
	for _, t := range level {
		ids.WriteString(t.id + "\n")
	}
	wrote := make(chan struct{})
	go func() {
		defer close(wrote)
		_, _ = io.WriteString(b.in, ids.String())
		_ = b.in.Close()
	}()
	// A write that git does not take ends when close drops the last read end
	// of the pipe.
	defer func() {
		b.close()
		<-wrote
	}()
	for i, t := range level {
		body, ok, err := b.answer("tree", t.id)
		if err != nil {
			return read, true
		}
		if ok {
			// An empty tree has a body of no bytes, which is not nil.
			read[i] = append([]byte{}, body...)
		}
	}
	return read, false
}
