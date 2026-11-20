package git

import (
	"bufio"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// fakeBatch is an objectBatch whose git answers with out. The requests go to
// a pipe that the test drops.
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
	b := &objectBatch{in: inW, inR: inR, outR: pr, out: bufio.NewReader(pr), done: done}
	t.Cleanup(b.close)
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
