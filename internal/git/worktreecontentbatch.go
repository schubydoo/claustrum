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
// call for the whole check. The long-running hash-object reads the object from
// the file objectBodyName in the folder of the objects of the check.
//
// After a stop c9125eb8 reads the trees of a level below the top with one
// `cat-file --buffer --batch` and hashes them with a new long-running
// hash-object of the trees (measured on Linux, readLevel). The top tree is read
// and hashed alone.

// longStoppedLine is the log line after a long-running git of the check
// stopped. Its first part is the git command, cat-file or hash-object, and the
// second is the cause (stopCause). The texts are of c9125eb8 (measured on
// Linux).
const longStoppedLine = "[GitHandler] worktree_create: stopped using the long-running git %s for the new worktree's config check: %s; the rest is read with a git process per object or per folder depth (slower, same result)"

// stopCause is the cause of longStoppedLine. The batch header "xyz blob" with
// no size gives the first text (c9125eb8, measured on Linux).
// The same text for the other answers that do not parse is claustrum's own.
// An end of the output gives "it stopped answering (EOF)" for the batch and
// for hash-object (measured on Linux).
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

// objectBodyName is the file that holds the object for the long-running
// hash-object, in the folder of the objects of the check (claustrum's own,
// not measured). The folder goes after the check with the file.
const objectBodyName = "object-body"

// objectBatch is one long-running git of the check: `cat-file --batch`, or a
// hash-object with --stdin-paths.
type objectBatch struct {
	in   *os.File // our end of the stdin of git
	inR  *os.File // the end of git, closed after git ended
	outR *io.PipeReader
	out  *bufio.Reader
	done chan struct{}
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
	c.batch = c.startLong(nil, "cat-file", "--batch")
}

// startLong starts one long-running git of the check in the base repository,
// with env after its environment. It gets the limit of one call, and its stop
// stops no other call (contentCheck.git). It is nil when no pipe opened.
func (c *contentCheck) startLong(env []string, args ...string) *objectBatch {
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil
	}
	s := c.u.stop
	if s != nil {
		s = &gitStop{wide: s.wide}
	}
	ctx := s.context()
	cmd := c.cmd(ctx, c.repo, args...)
	cmd.Env = append(cmd.Env, env...)
	outR, outW := io.Pipe()
	cmd.Stdin, cmd.Stdout = inR, outW
	b := &objectBatch{in: inW, inR: inR, outR: outR, out: bufio.NewReader(outR), done: make(chan struct{})}
	go func() {
		defer close(b.done)
		_ = gitRun(ctx, cmd)
		// Any end of git reads as an end of its output (io.EOF).
		_ = outW.Close()
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
	_ = b.in.Close()
	_ = b.outR.Close()
	<-b.done
	_ = b.inR.Close()
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
	return strings.TrimSpace(line), nil
}

// stopBatch ends the batch and the long-running hash-object calls of the
// check, if they run.
func (c *contentCheck) stopBatch() {
	if c.batch != nil {
		c.batch.close()
		c.batch = nil
	}
	for kind, h := range c.hashers {
		h.close()
		delete(c.hashers, kind)
	}
}

// hashLong hashes body as an object of kind with the long-running hash-object
// of that kind, which it starts at the first object of the kind.
func (c *contentCheck) hashLong(kind string, body []byte) (string, error) {
	id, h, err := c.hashOn(c.hashers[kind], kind, body)
	if h != nil && c.hashers[kind] == nil {
		if c.hashers == nil {
			c.hashers = map[string]*objectBatch{}
		}
		c.hashers[kind] = h
	}
	return id, err
}

// hashOn hashes body as an object of kind with h, a long-running hash-object
// of that kind. A nil h starts one. h is the one that ran, or nil.
func (c *contentCheck) hashOn(h *objectBatch, kind string, body []byte) (string, *objectBatch, error) {
	p := filepath.Join(c.objects, objectBodyName)
	if strings.ContainsAny(p, "\r\n") {
		return "", h, errNoHashHelper
	}
	if err := writeObjectBody(c.objects, body); err != nil {
		return "", h, errNoHashHelper
	}
	if h == nil {
		h = c.startLong(objectWriteEnv(c.objects), "hash-object", "-t", kind, "--literally", "--no-filters", "--stdin-paths", "-w")
		if h == nil {
			return "", nil, errNoHashHelper
		}
	}
	id, err := h.hash(p)
	return id, h, err
}

// writeObjectBody writes body to objectBodyName in dir, through a root at dir.
// Tests replace it.
var writeObjectBody = func(dir string, body []byte) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.WriteFile(objectBodyName, body, 0o600)
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
	if c.batch != nil && !c.stopped {
		body, ok, err := c.batch.read(kind, id)
		switch {
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
// Claustrum's own, not measured: the trees of one level go to the read in the
// order of the walk, a tree of that read does not count in c.separate, the
// hash-object of the level ends with the level, no second one starts in a level
// after a stop, and a tree whose body cannot be written for it is hashed by a
// separate run.
func (c *contentCheck) readLevel(level []*treeWalk) ([][]byte, string) {
	read := make([][]byte, len(level))
	if c.stopped && !level[0].top {
		read = c.readByDepth(level)
	}
	bodies := make([][]byte, len(level))
	var h *objectBatch
	defer func() {
		if h != nil {
			h.close()
		}
	}()
	hashing := true
	for i, t := range level {
		if body := read[i]; body != nil && hashing {
			got, ran, err := c.hashOn(h, "tree", body)
			h = ran
			switch {
			case err == nil:
				if text := hashText("tree", t.id, got); text != "" {
					return nil, text
				}
				c.read++
				bodies[i] = body
				continue
			case errors.Is(err, errNoHashHelper):
				if text := c.writeObject("tree", t.id, body); text != "" {
					return nil, text
				}
				c.read++
				bodies[i] = body
				continue
			}
			// The stopped git ends here, so a later tree of the level would
			// start a new one without the flag.
			h.close()
			h = nil
			hashing = false
		}
		body, text := c.readObject("tree", t.id)
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
// answer that does not parse or does not come.
func (c *contentCheck) readByDepth(level []*treeWalk) [][]byte {
	read := make([][]byte, len(level))
	b := c.startLong(nil, "cat-file", "--buffer", "--batch")
	if b == nil {
		return read
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
			break
		}
		if ok {
			// An empty tree has a body of no bytes, which is not nil.
			read[i] = append([]byte{}, body...)
		}
	}
	return read
}
