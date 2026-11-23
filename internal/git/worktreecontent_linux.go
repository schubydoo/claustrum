package git

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// helperKillAtDeadline turns on the kill of worktreecontentkill.go, which rows
// measure on Linux only.
const helperKillAtDeadline = true

// leafLinkErr follows the link at path in leaf through a root at leaf. It
// returns the error of a link that the root cannot follow. A loop gives "too
// many levels of symbolic links", and a link out of leaf gives "path escapes
// from parent" (b3081b51, measured on Linux). A target that is gone, and a
// root that cannot be opened, give nil (claustrum's own, not measured).
func leafLinkErr(leaf, path string) error {
	r, err := os.OpenRoot(leaf)
	if err != nil {
		return nil
	}
	defer func() { _ = r.Close() }()
	_, err = r.Stat(filepath.FromSlash(path))
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
