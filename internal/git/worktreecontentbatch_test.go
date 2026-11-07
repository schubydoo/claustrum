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
