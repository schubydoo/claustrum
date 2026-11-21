package files

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/schubydoo/claustrum/internal/pathcanon"
	"github.com/schubydoo/claustrum/internal/rpc"
)

// realpathParams holds the members that files.realpath reads. ExpandPaths does
// nothing: the handler tests the raw path for an unclean "~/" form first, and
// then calls pathcanon.ExpandTilde itself.
type realpathParams struct {
	Path        string `json:"path"`
	MissingLeaf bool   `json:"missingLeaf"`
}

func (p *realpathParams) ExpandPaths() {}

// realpathErrData is the data member of a -32603 reply of files.realpath.
// StoppedShort is set for too_many_links on Windows (loopStopsShort).
type realpathErrData struct {
	Reason       string `json:"reason"`
	Errno        int    `json:"errno"`
	StoppedShort bool   `json:"stoppedShort,omitempty"`
}

const (
	realpathNeedAbsolute = "files.realpath: the path must be absolute"
	realpathHasNUL       = "files.realpath: the path contains a NUL character"
	realpathUncleanTilde = `files.realpath: a path that starts with "~/" must have no "." or ".." component, no "//" and no trailing slash`
)

// uncleanTilde reports whether a path that starts with "~/" has a "." or ".."
// component, an empty component ("//" or a trailing slash). The rows measure the
// ".." case only. The other cases are claustrum's own, not measured.
func uncleanTilde(p string) bool {
	if !strings.HasPrefix(p, "~/") {
		return false
	}
	rest := p[2:]
	if runtime.GOOS == "windows" {
		rest = strings.ReplaceAll(rest, `\`, "/")
	}
	for _, c := range strings.Split(rest, "/") {
		if c == "" || c == "." || c == ".." {
			return true
		}
	}
	return false
}

// filesRealpath answers files.realpath on build 683cc6a9. It makes no write and
// no delete. The real name comes from realpathEval (claustrum's own).
func filesRealpath(req *rpc.Request) rpc.Response {
	var p realpathParams
	if bad := rpc.Bind(req, &p); bad != nil {
		return *bad
	}
	if strings.ContainsRune(p.Path, 0) {
		return rpc.ErrResult(req.ID, rpc.CodeInvalidParam, realpathHasNUL)
	}
	if uncleanTilde(p.Path) {
		return rpc.ErrResult(req.ID, rpc.CodeInvalidParam, realpathUncleanTilde)
	}
	path := pathcanon.ExpandTilde(p.Path)
	if msg := realpathPathRefusal(path); msg != "" {
		return rpc.ErrResult(req.ID, rpc.CodeInvalidParam, msg)
	}
	res, err := resolveRealpath(path, p.MissingLeaf)
	if err != nil {
		return realpathFailure(req.ID, err)
	}
	return rpc.OKResult(req.ID, res)
}

// resolveRealpath builds the result. A path that exists gives real. A missing
// path with missingLeaf gives wouldBe, the resolved parent plus the last name.
// A missing parent with missingLeaf gives plain exists:false (claustrum's own,
// not measured).
// A dangling link with missingLeaf gives dangling, and wouldBe only if the parent
// of its target exists. A dangling link without missingLeaf gives the plain
// exists:false (claustrum's own, not measured). On Windows a missing path first
// takes the failure of its nearest existing folder, or the error 1921 of a
// folder on the way (realpathMissingFailure).
//
// A path through a regular file, as <file>/a, gives the plain exists:false, with
// and without missingLeaf (4c111e86, measured on Linux, and for <file>/a with no
// missingLeaf on macOS). A dangling link to such a path gives dangling with no
// wouldBe (4c111e86, measured on Linux). The system answers ENOTDIR there, which
// counts as a missing name.
//
// A trailing "/" after a file, a link to a file or a FIFO, as in <file>/ and
// <file>//, gives the plain exists:false, with and without missingLeaf
// (4c111e86, measured on Linux; macOS is claustrum's own, not measured). With it,
// a folder and a link to a folder give the folder as real. A dangling link gives
// exists:false, and with missingLeaf dangling and the wouldBe of its target. A
// missing name gives exists:false, and with missingLeaf its wouldBe (4c111e86,
// measured on Linux).
func resolveRealpath(path string, missingLeaf bool) (rpc.RealpathReply, error) {
	slash := realpathEndsInSep(path)
	path = realpathClean(path)
	fi, err := os.Stat(path)
	if err == nil && slash && !fi.IsDir() {
		return rpc.RealpathReply{}, nil
	}
	var real string
	if err == nil {
		real, err = realpathEval(path)
	}
	if err == nil {
		return rpc.RealpathReply{Exists: true, Real: filepath.ToSlash(real)}, nil
	}
	if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
		return rpc.RealpathReply{}, err
	}
	if err := realpathMissingFailure(path); err != nil {
		return rpc.RealpathReply{}, err
	}
	if !missingLeaf {
		return rpc.RealpathReply{}, nil
	}
	if fi, lerr := os.Lstat(path); lerr == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return resolveDangling(path), nil
	}
	if wouldBe, ok := wouldBeUnder(path); ok {
		return rpc.RealpathReply{WouldBe: wouldBe}, nil
	}
	return rpc.RealpathReply{}, nil
}

// splitLast splits an absolute path at its last separator without cleaning it.
// On Linux and macOS the parent keeps its ".." elements, so a ".." after a link
// is applied to the link target when EvalSymlinks resolves it. On Windows the
// text is already clean (realpathClean). A trailing separator is dropped.
func splitLast(path string) (parent, last string) {
	seps := "/"
	if runtime.GOOS == "windows" {
		seps = `/\`
	}
	path = strings.TrimRight(path, seps)
	// Every caller passes an absolute path, so a separator is always there.
	i := max(strings.LastIndexAny(path, seps), 0)
	parent = path[:i]
	if parent == "" || strings.HasSuffix(parent, ":") {
		parent = path[:i+1]
	}
	return parent, path[i+1:]
}

// resolveDangling never fails. The answers of the system for the folders on the
// way were all good when the caller got here, so a later failure (a race) gives
// the answer with no wouldBe (claustrum's own, not measured).
func resolveDangling(path string) rpc.RealpathReply {
	res := rpc.RealpathReply{Dangling: true}
	target, err := os.Readlink(path)
	if err == nil && !filepath.IsAbs(target) {
		// Linux and macOS: the kernel reads a relative target from the real folder
		// of the link, and the text is not cleaned, so a ".." after a link follows
		// the kernel. Windows: the target text is cleaned by text, as Win32 does
		// (realpathClean). Claustrum's own, not measured.
		folder, _ := splitLast(path)
		var dir string
		dir, err = realpathEval(folder)
		target = realpathJoin(dir, target)
	}
	target = realpathClean(target)
	if err == nil {
		res.WouldBe, _ = wouldBeUnder(target)
	}
	return res
}

// wouldBeUnder resolves the parent folder of path and adds the last name. ok is
// false when the parent cannot be resolved, or resolves to no folder, as a regular
// file (4c111e86, measured on Linux). The caller tested the path first, so only a
// missing parent, a parent that is a file (or a race) gets here. The last name is
// "." or ".." only below a parent that is no folder, as <file>/., because below a
// folder such a path exists. The folder test then gives ok false.
func wouldBeUnder(path string) (string, bool) {
	parent, last := splitLast(path)
	real, err := realpathEval(parent)
	if err != nil {
		return "", false
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return "", false
	}
	return filepath.ToSlash(filepath.Join(real, last)), true
}

// realpathReasonData is the data member of the no_usable_name reply, which has
// no errno. On Windows it has atLetter "share" for a path whose own drive is a
// network drive, and stoppedShort true for a path that reaches the network
// through a link (87375cc7, measured on Windows).
type realpathReasonData struct {
	Reason       string `json:"reason"`
	AtLetter     string `json:"atLetter,omitempty"`
	StoppedShort bool   `json:"stoppedShort,omitempty"`
}

// errNoUsableName is the answer of realpathEval on Windows for a path on a
// mapped network drive, and for a link to one or to a `\\host\share`, `\\?\UNC\`
// or `\\.\UNC\` path (683cc6a9 and 4c111e86). Other network forms are
// claustrum's own, not measured.
var errNoUsableName = errors.New("no verified path for the target (it may be deleted or moved, or on Windows on a network share)")

// errNoUsableNameAtLetter is errNoUsableName for a path whose own drive letter
// names a network drive, before any link is followed (realpathEval on Windows).
var errNoUsableNameAtLetter = fmt.Errorf("%w", errNoUsableName)

// realpathFailure maps a failed call to the -32603 reply. Windows no_permission
// is not measured. An error with no errno gets reason other and errno 0
// (claustrum's own, not measured).
func realpathFailure(id interface{}, err error) rpc.Response {
	if errors.Is(err, errNoUsableName) {
		data := realpathReasonData{Reason: "no_usable_name", StoppedShort: true}
		if errors.Is(err, errNoUsableNameAtLetter) {
			data = realpathReasonData{Reason: "no_usable_name", AtLetter: "share"}
		}
		return rpc.ErrResultData(id, rpc.CodeInternal, "files.realpath: "+err.Error(), data)
	}
	var en syscall.Errno
	if !errors.As(err, &en) {
		return rpc.ErrResultData(id, rpc.CodeInternal, "files.realpath: "+err.Error(),
			realpathErrData{Reason: "other"})
	}
	reason := "other"
	switch {
	case isLinkLoopErrno(en):
		reason = "too_many_links"
	case errors.Is(err, fs.ErrPermission):
		reason = "no_permission"
	}
	return rpc.ErrResultData(id, rpc.CodeInternal,
		"files.realpath: error "+strconv.Itoa(int(en))+": "+en.Error(),
		realpathErrData{Reason: reason, Errno: int(en), StoppedShort: reason == "too_many_links" && loopStopsShort})
}
