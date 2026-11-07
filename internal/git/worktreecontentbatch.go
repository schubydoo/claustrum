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
// on Linux and macOS). The batch runs on every system. On Windows the call is
// not measured, but the checked line counts 0 separate runs there (c9125eb8,
// measured), which fits one batch. A git that stops answering, or a process
// that cannot compute SHA-1, ends that use. Then one `cat-file <kind> <id>`
// reads each object, with the same result (measured on Linux). The checked
// line counts the commits and trees that separate runs read.
//
// While the batch runs, c9125eb8 hashes the commits with one long-running
// `hash-object -t commit --literally --no-filters --stdin-paths -w` and the
// trees with one such call of `-t tree` (measured on Linux, macOS and
// Windows). Each starts at the first object of its kind and ends with the
// batch (measured on Linux). After a hash-object that stopped answering, the
// batch ends too, and separate runs read and hash that object and the rest:
// `cat-file <kind> <id>`, then `hash-object -t <kind> --literally
// --no-filters --stdin -w` (c9125eb8, measured on Linux).
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
// After a stop c9125eb8 reads the trees of a level below the top with `cat-file
// --buffer --batch` (measured on Linux). Claustrum reads each of them alone.

// longStoppedLine is the log line after a long-running git of the check
// stopped. Its first part is the git command, cat-file or hash-object, and the
// second is the cause (stopCause). The texts are of c9125eb8 (measured on
// Linux).
const longStoppedLine = "[GitHandler] worktree_create: stopped using the long-running git %s for the new worktree's config check: %s; the rest is read with a git process per object or per folder depth (slower, same result)"

// stopCause is the cause of longStoppedLine. A batch that answered with the
// header of another object gives the first text (c9125eb8, measured on Linux).
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
func (c *contentCheck) startBatch() {
	if !sha1Works() {
		logging.Infof("%s", noSHA1Line)
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
	p := filepath.Join(c.objects, objectBodyName)
	if strings.ContainsAny(p, "\r\n") {
		return "", errNoHashHelper
	}
	if err := writeObjectBody(c.objects, body); err != nil {
		return "", errNoHashHelper
	}
	h := c.hashers[kind]
	if h == nil {
		h = c.startLong(objectWriteEnv(c.objects), "hash-object", "-t", kind, "--literally", "--no-filters", "--stdin-paths", "-w")
		if h == nil {
			return "", errNoHashHelper
		}
		if c.hashers == nil {
			c.hashers = map[string]*objectBatch{}
		}
		c.hashers[kind] = h
	}
	return h.hash(p)
}

// writeObjectBody writes body to objectBodyName in dir, through a root at dir.
func writeObjectBody(dir string, body []byte) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.WriteFile(objectBodyName, body, 0o600)
}

// objectWriteEnv is the environment after the environment of a hash-object
// call with -w: its objects go to dir, with no alternates (b3081b51 and
// c9125eb8, measured on Linux).
func objectWriteEnv(dir string) []string {
	return []string{"GIT_OBJECT_DIRECTORY=" + dir, "GIT_ALTERNATE_OBJECT_DIRECTORIES="}
}

// catFile reads the object id of kind: through the batch while it answers,
// and by a separate `cat-file <kind> <id>` run otherwise. A commit or a tree
// that a separate run read counts in c.separate.
func (c *contentCheck) catFile(kind, id string) ([]byte, string, error) {
	if c.batch != nil {
		body, ok, err := c.batch.read(kind, id)
		switch {
		case err != nil:
			logging.Infof(longStoppedLine, "cat-file", stopCause(err))
			c.stopBatch()
		case ok:
			return body, "", nil
		}
	}
	body, stderr, err := c.git(c.repo, nil, nil, "cat-file", kind, id)
	if err == nil && kind != "blob" {
		c.separate++
	}
	return body, stderr, err
}
