package git

import (
	"path/filepath"
	"testing"
)

// A leaf that is there makes the leaf step fail on every system. A second create
// of the same leaf that starts during the first then makes no checkpoint of the
// leaf of the first, so no rollback of it removes that leaf (claustrum's own,
// not measured). Mutation: os.MkdirAll in the Windows mkdirWorktreeLeaf.
func TestMkdirWorktreeLeafRefusesALeafThatIsThere(t *testing.T) {
	leaf := filepath.Join(t.TempDir(), "w1")
	if err := mkdirWorktreeLeaf(leaf); err != nil {
		t.Fatal(err)
	}
	if err := mkdirWorktreeLeaf(leaf); err == nil {
		t.Error("the second mkdirWorktreeLeaf of the same leaf = nil, want an error")
	}
}
