package git

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/schubydoo/claustrum/internal/logging"
)

// pathLookRefusal is the refusal of a repository path that the network-path
// check cannot look at, or "". It runs only with networkLinkChecks.
//
// 4c111e86 answers it for a folder with the reparse tag IO_REPARSE_TAG_WIM,
// which no filter of the system handles (measured on Windows). A repository below
// such a folder gets it on git.info, git.status and git.list_branches, and the
// text names the path. A repository whose .git is such a folder gets it on all
// five methods, and the text names <path>\.git\objects. No git call on the
// repository runs, and the daemon logs a network-path line.
//
// claustrum's own, not measured: only that error refuses
// (lookCannotAccess), the walk tests each name of the path and then the objects
// folder of a .git folder at the path, and the text names the whole path.
func pathLookRefusal(path string) string {
	if !networkLinkChecks {
		return ""
	}
	p, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	vol := filepath.VolumeName(p)
	cur := vol + string(filepath.Separator)
	for _, name := range strings.FieldsFunc(p[len(vol):], isPathSep) {
		cur = filepath.Join(cur, name)
		if _, err := lstatPath(cur); err != nil {
			return lookFailure(path, err)
		}
	}
	if fi, err := lstatPath(filepath.Join(p, ".git")); err != nil || !fi.IsDir() {
		return ""
	}
	objects := filepath.Join(p, ".git", "objects")
	if _, err := lstatPath(objects); err != nil {
		return lookFailure(objects, err)
	}
	return ""
}

// lookFailure logs the network-path line and gives the text for a failed look
// at path, when err is the error of the row. Any other error gives "".
func lookFailure(path string, err error) string {
	if !lookCannotAccess(err) {
		return ""
	}
	detail := err
	if pe := (*fs.PathError)(nil); errors.As(err, &pe) {
		detail = pe.Err
	}
	p := filepath.Clean(path)
	logging.Infof("[GitHandler] network-path check of %q: could not determine where the path's links point: %s", p, detail.Error())
	return "Claude couldn't check that `" + p + "` stays on a local drive, so git wasn't run. Try again."
}
