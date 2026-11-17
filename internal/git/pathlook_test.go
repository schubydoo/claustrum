package git

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
)

// cannotAccess stands for ERROR_CANT_ACCESS_FILE in the tests. Its text is the
// one of the system.
type cannotAccess struct{}

func (cannotAccess) Error() string { return "The file cannot be accessed by the system." }

var errCannotAccess error = cannotAccess{}

// The two shapes of the row of 4c111e86 on Windows, byte for byte: a repository
// below a folder that cannot be looked into, and a repository whose .git is
// such a folder. Each look logs one network-path line. A missing path, a plain
// repository and another error pass.
func TestPathLookRefusal(t *testing.T) {
	dir, err := os.Lstat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	below, gitWim := "/w/F_b_wim/S/P/repo", "/w/F_a_wim/R"
	failed := map[string]error{
		below:                    errCannotAccess,
		gitWim + "/.git/objects": errCannotAccess,
		"/w/other/repo":          errors.New("another error"),
		"/w/gone":                fs.ErrNotExist,
	}
	oldLstat, oldOn, oldAccess := lstatPath, networkLinkChecks, lookCannotAccess
	lstatPath = func(path string) (fs.FileInfo, error) {
		if err := failed[path]; err != nil {
			return nil, &fs.PathError{Op: "CreateFile", Path: path, Err: err}
		}
		if strings.HasPrefix(path, "/w/gone") {
			return nil, &fs.PathError{Op: "CreateFile", Path: path, Err: fs.ErrNotExist}
		}
		return dir, nil
	}
	lookCannotAccess = func(err error) bool { return errors.Is(err, errCannotAccess) }
	networkLinkChecks = true
	t.Cleanup(func() { lstatPath, networkLinkChecks, lookCannotAccess = oldLstat, oldOn, oldAccess })
	buf := captureLogBuf(t)
	text := func(p string) string {
		return "Claude couldn't check that `" + p + "` stays on a local drive, so git wasn't run. Try again."
	}
	for _, c := range []struct{ path, want string }{
		{below, text(below)},
		{gitWim, text(gitWim + "/.git/objects")},
		{"/w/other/repo", ""},
		{"/w/gone/repo", ""},
		{"/w/plain/R", ""},
	} {
		if got := pathLookRefusal(c.path); got != c.want {
			t.Errorf("%s: got %q, want %q", c.path, got, c.want)
		}
	}
	// networkLinkRefusal runs the test too.
	if got := networkLinkRefusal(below); got != text(below) {
		t.Errorf("networkLinkRefusal: got %q", got)
	}
	line := func(p string) string {
		return `[GitHandler] network-path check of "` + p + `": could not determine where the path's links point: The file cannot be accessed by the system.` + "\n"
	}
	log := buf.String()
	if n := strings.Count(log, line(below)); n != 2 {
		t.Errorf("got %d lines for %s, want 2:\n%s", n, below, log)
	}
	if n := strings.Count(log, line(gitWim+"/.git/objects")); n != 1 {
		t.Errorf("got %d lines for the objects folder, want 1:\n%s", n, log)
	}
	if n := strings.Count(log, "network-path check of"); n != 3 {
		t.Errorf("got %d network-path lines, want 3:\n%s", n, log)
	}
	networkLinkChecks = false
	if got := pathLookRefusal(below); got != "" {
		t.Errorf("test off: got %q", got)
	}
}
