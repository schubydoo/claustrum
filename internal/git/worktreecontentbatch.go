package git

import (
	"bufio"
	"bytes"
	"crypto/sha1" //nolint:gosec // a probe of the fips140 mode, no hash of data
	"errors"
	"io"
	"os"
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
// Claustrum's own, not measured: the blobs of committed links go through the
// batch too and do not count. The blob of the compare step is always a
// separate run. An object that the batch names missing, ambiguous or of
// another kind is read again by a separate run, and the batch goes on. A
// malformed answer, a failed start and a stop at the limit end the batch as an
// end of its output does. The batch has the limit of one call for the whole
// check.

// batchStoppedLine is the log line after the batch stopped answering. Its
// cause is "EOF" for an end of the output (c9125eb8, measured on Linux).
const batchStoppedLine = "[GitHandler] worktree_create: stopped using the long-running git cat-file for the new worktree's config check: it stopped answering (%v); the rest is read with a git process per object or per folder depth (slower, same result)"

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

// errBatchAnswer is the cause of an answer of the batch that does not parse
// (claustrum's own text, not measured).
var errBatchAnswer = errors.New("an answer that cannot be read")

// objectBatch is the running `git cat-file --batch` of one check.
type objectBatch struct {
	in   *os.File // our end of the stdin of git
	inR  *os.File // the end of git, closed after git ended
	outR *io.PipeReader
	out  *bufio.Reader
	done chan struct{}
}

// startBatch starts the batch of the check in the base repository, with the
// config listing before it, as each call of the check. It gets the limit of
// one call, and its stop stops no other call (contentCheck.git). A failed
// start shows at the first read as an end of the output.
func (c *contentCheck) startBatch() {
	if !sha1Works() {
		logging.Infof("%s", noSHA1Line)
		return
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		// No pipe: every object is read by a separate run, with no line
		// (claustrum's own, not measured).
		return
	}
	s := c.u.stop
	if s != nil {
		s = &gitStop{wide: s.wide}
	}
	ctx := s.context()
	cmd := hardenedGitCmd(ctx, c.u, c.repo, false, nil, "cat-file", "--batch")
	outR, outW := io.Pipe()
	cmd.Stdin, cmd.Stdout = inR, outW
	b := &objectBatch{in: inW, inR: inR, outR: outR, out: bufio.NewReader(outR), done: make(chan struct{})}
	go func() {
		defer close(b.done)
		_ = gitRun(ctx, cmd)
		// Any end of git reads as an end of its output (io.EOF).
		_ = outW.Close()
	}()
	c.batch = b
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

// stopBatch ends the batch of the check, if one runs.
func (c *contentCheck) stopBatch() {
	if c.batch != nil {
		c.batch.close()
		c.batch = nil
	}
}

// catFile reads the object id of kind: through the batch while it answers,
// and by a separate `cat-file <kind> <id>` run otherwise. A commit or a tree
// that a separate run read counts in c.separate.
func (c *contentCheck) catFile(kind, id string) ([]byte, string, error) {
	if c.batch != nil {
		body, ok, err := c.batch.read(kind, id)
		switch {
		case err != nil:
			logging.Infof(batchStoppedLine, err)
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
