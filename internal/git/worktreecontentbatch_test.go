package git

import (
	"bufio"
	"errors"
	"io"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeBatch is an objectBatch whose git answers with out. The requests go to
// a pipe that the test drops at its end.
func fakeBatch(t *testing.T, out string) *objectBatch {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	pr, pw := io.Pipe()
	go func() {
		_, _ = io.WriteString(pw, out)
		_ = pw.Close()
	}()
	done := make(chan struct{})
	close(done)
	// The test keeps the read end of the requests, as a git that runs would.
	b := &objectBatch{in: inW, outR: pr, out: bufio.NewReader(pr), done: done}
	t.Cleanup(func() { b.close(); _ = inR.Close() })
	return b
}

// TestObjectBatchRead: the parse of the answers of `cat-file --batch`. A good
// answer gives the body. A missing object and one of another kind ask for a
// separate run, and the batch goes on. An end of the output gives io.EOF, and
// a malformed header ends the batch (claustrum's own, not measured).
func TestObjectBatchRead(t *testing.T) {
	id := strings.Repeat("a", 40)
	other := strings.Repeat("b", 40)
	b := fakeBatch(t, id+" tree 3\nxyz\n"+id+" missing\n"+id+" blob 1\nq\n")
	if body, ok, err := b.read("tree", id); err != nil || !ok || string(body) != "xyz" {
		t.Fatalf("read of a tree = %q, %v, %v; want xyz", body, ok, err)
	}
	if _, ok, err := b.read("tree", id); err != nil || ok {
		t.Fatalf("read of a missing object = %v, %v; want a separate run", ok, err)
	}
	if _, ok, err := b.read("tree", id); err != nil || ok {
		t.Fatalf("read of a blob as a tree = %v, %v; want a separate run", ok, err)
	}
	if _, _, err := b.read("tree", id); !errors.Is(err, io.EOF) {
		t.Fatalf("read after the end = %v, want EOF", err)
	}
	for _, out := range []string{
		other + " tree 3\nxyz\n", // another id
		id + " tree x\n",         // no size
		id + " tree 3\nxyzw",     // no line end after the body
	} {
		if _, _, err := fakeBatch(t, out).read("tree", id); !errors.Is(err, errBatchAnswer) {
			t.Errorf("read of %q = %v, want %v", out, err, errBatchAnswer)
		}
	}
	// A body shorter than its size is an end of the output.
	if _, _, err := fakeBatch(t, id+" tree 9\nxy").read("tree", id); !errors.Is(err, io.EOF) {
		t.Errorf("read of a short body = %v, want EOF", err)
	}
	// A false size header reserves no memory: the body grows as it comes.
	if _, _, err := fakeBatch(t, id+" tree 4611686018427387904\nxy").read("tree", id); !errors.Is(err, io.EOF) {
		t.Errorf("read of a huge false size = %v, want EOF", err)
	}
}

// TestHashOnNoObjectID: an id that is no object id takes no file in the
// scratch folder and gets no answer of the long-running hash-object. The
// caller sends the object to a separate run, and the git that ran stays for
// the next object. A nil git is not started (claustrum's own, not measured).
func TestHashOnNoObjectID(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	old := writeObjectBody
	t.Cleanup(func() { writeObjectBody = old })
	var wrote []string
	writeObjectBody = func(r *os.Root, name string, body []byte) error {
		wrote = append(wrote, name)
		return old(r, name, body)
	}
	c := &contentCheck{scratch: root}
	h := fakeBatch(t, strings.Repeat("a", 40)+"\n")
	for _, id := range []string{"not-an-id", strings.Repeat("g", 40), strings.Repeat("a", 39), ""} {
		if got, ran, err := c.hashOn(h, "blob", id, []byte("x")); got != "" || ran != h || !errors.Is(err, errNoHashHelper) {
			t.Errorf("hashOn(%q) = %q, %p, %v; want no answer, the same git %p and %v", id, got, ran, err, h, errNoHashHelper)
		}
	}
	if got, ran, err := c.hashOn(nil, "blob", "not-an-id", []byte("x")); got != "" || ran != nil || !errors.Is(err, errNoHashHelper) {
		t.Errorf("hashOn with no git = %q, %p, %v; want no git started and %v", got, ran, err, errNoHashHelper)
	}
	if len(wrote) != 0 {
		t.Errorf("files written for %q, want none", wrote)
	}
}

// TestOpenScratchRootDefaultsToNoWait keeps the scratch folder under the guard of
// TestFoldersOfTheDaemonOpenWithNoWait: that scan sees calls of OpenRoot only, and the
// seam hides the default open from it.
func TestOpenScratchRootDefaultsToNoWait(t *testing.T) {
	if reflect.ValueOf(openScratchRoot).Pointer() != reflect.ValueOf(openRootNoWait).Pointer() {
		t.Fatal("openScratchRoot must default to openRootNoWait, so the scratch folder opens with no wait on a FIFO")
	}
}

// TestObjectBatchWriteAfterItsGitEnded: after the first answer of a
// long-running git the daemon holds no read end of its stdin. So a request to a
// batch whose git ended after one answer fails at its write with "write |1:
// broken pipe", the cause that 4c111e86 logs there (measured on Linux).
// Mutation: keep the read end of the pipe open in the daemon (the read gives
// EOF).
func TestObjectBatchWriteAfterItsGitEnded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the text of a broken pipe is that of Linux and macOS")
	}
	f := newContentFixture(t, map[string]string{".mcp.json": "{}\n"})
	id := strings.Repeat("a", 40)
	slowGit(t, "cat-file,--batch", "fail", 0, "", id+" commit 1\nx\n")
	c := &contentCheck{repo: f.repo}
	b := c.startLong(nil, "cat-file", "--batch")
	if b == nil {
		t.Fatal("no batch started")
	}
	if body, ok, err := b.read("commit", id); err != nil || !ok || string(body) != "x" {
		t.Fatalf("first read = %q, %v, %v, want the answer x", body, ok, err)
	}
	<-b.done
	_, _, err := b.read("commit", id)
	if err == nil || stopCause(err) != "it stopped answering (write |1: broken pipe)" {
		t.Errorf("read after the end of git = %v, want the broken pipe", err)
	}
	b.close()
}

// TestObjectBatchDropClosesStdinReadEnd: a batch dropped before its first
// answer closes the copy of the daemon of the read end of the stdin of git
// once git ended (claustrum's own). Mutation: no close at the end of git (the
// end stays open).
func TestObjectBatchDropClosesStdinReadEnd(t *testing.T) {
	f := newContentFixture(t, map[string]string{".mcp.json": "{}\n"})
	gate := gateStub(t, "cat-file,--batch", 30*time.Second, "", "")
	c := &contentCheck{repo: f.repo}
	b := c.startLong(nil, "cat-file", "--batch")
	if b == nil {
		t.Fatal("no batch started")
	}
	b.drop()
	_ = os.WriteFile(gate, nil, 0o644)
	<-b.done
	if _, err := b.inR.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Errorf("the read end of the stdin of git after the drop and the end of git: %v, want it closed", err)
	}
}
