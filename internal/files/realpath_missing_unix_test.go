//go:build unix

package files

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestResolveRealpathMissingFailureComesFirst pins the shared step of
// resolveRealpath: the failure of the nearest existing folder (the Windows walk,
// stubbed here) comes back before the missingLeaf rules, with and without
// missingLeaf. Mutation: resolveRealpath with no realpathMissingFailure call.
// The missing path then gives the plain or wouldBe reply and no error.
func TestResolveRealpathMissingFailureComesFirst(t *testing.T) {
	old := realpathMissingFailure
	t.Cleanup(func() { realpathMissingFailure = old })
	realpathMissingFailure = func(string) error { return errNoUsableName }
	path := filepath.Join(t.TempDir(), "nodir", "x")
	for _, missingLeaf := range []bool{false, true} {
		reply, err := resolveRealpath(path, missingLeaf)
		if !errors.Is(err, errNoUsableName) {
			t.Errorf("missingLeaf %v: reply %+v, err %v, want errNoUsableName", missingLeaf, reply, err)
		}
	}
}
