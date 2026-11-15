package git

import (
	"errors"
	"os"
)

// rmdirHeadCopy removes nothing on Windows: a folder at the copy of HEAD stays,
// and the request fails as before (claustrum's own, not measured).
func rmdirHeadCopy(_ *os.Root, _ string) error {
	return errors.New("a folder at the copy of HEAD is not removed on Windows")
}
