package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// realpathParams holds the members that files.realpath reads. expandPaths does
// nothing: the handler tests the raw path for an unclean "~/" form first, and
// then calls expandPath itself.
type realpathParams struct {
	Path        string `json:"path"`
	MissingLeaf bool   `json:"missingLeaf"`
}

func (p *realpathParams) expandPaths() {}

// realpathErrData is the data member of a -32603 reply of files.realpath.
type realpathErrData struct {
	Reason string `json:"reason"`
	Errno  int    `json:"errno"`
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
// no delete. The kernel decides the link limit, through os.Stat. The real name
// comes from filepath.EvalSymlinks (claustrum's own).
func filesRealpath(req *request) response {
	var p realpathParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	if strings.ContainsRune(p.Path, 0) {
		return errResult(req.ID, codeInvalidParam, realpathHasNUL)
	}
	if uncleanTilde(p.Path) {
		return errResult(req.ID, codeInvalidParam, realpathUncleanTilde)
	}
	path := expandPath(p.Path)
	if msg := realpathPathRefusal(path); msg != "" {
		return errResult(req.ID, codeInvalidParam, msg)
	}
	res, err := resolveRealpath(path, p.MissingLeaf)
	if err != nil {
		return realpathFailure(req.ID, err)
	}
	return okResult(req.ID, res)
}

// resolveRealpath builds the result. A path that exists gives real. A missing
// path with missingLeaf gives wouldBe, the resolved parent plus the last name.
// A missing parent with missingLeaf gives plain exists:false (claustrum's own,
// not measured).
// A dangling link with missingLeaf gives dangling, and wouldBe only if the parent
// of its target exists. A dangling link without missingLeaf gives the plain
// exists:false (claustrum's own, not measured).
func resolveRealpath(path string, missingLeaf bool) (realpathResult, error) {
	path = realpathClean(path)
	_, err := os.Stat(path)
	var real string
	if err == nil {
		real, err = filepath.EvalSymlinks(path)
	}
	if err == nil {
		return realpathResult{Exists: true, Real: filepath.ToSlash(real)}, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return realpathResult{}, err
	}
	if !missingLeaf {
		return realpathResult{}, nil
	}
	if fi, lerr := os.Lstat(path); lerr == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return resolveDangling(path), nil
	}
	if wouldBe, ok := wouldBeUnder(path); ok {
		return realpathResult{WouldBe: wouldBe}, nil
	}
	return realpathResult{}, nil
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
func resolveDangling(path string) realpathResult {
	res := realpathResult{Dangling: true}
	target, err := os.Readlink(path)
	if err == nil && !filepath.IsAbs(target) {
		// Linux and macOS: the kernel reads a relative target from the real folder
		// of the link, and the text is not cleaned, so a ".." after a link follows
		// the kernel. Windows: the target text is cleaned by text, as Win32 does
		// (realpathClean). Claustrum's own, not measured.
		folder, _ := splitLast(path)
		var dir string
		dir, err = filepath.EvalSymlinks(folder)
		target = realpathJoin(dir, target)
	}
	target = realpathClean(target)
	if err == nil {
		res.WouldBe, _ = wouldBeUnder(target)
	}
	return res
}

// wouldBeUnder resolves the parent folder of path and adds the last name. ok is
// false when the parent cannot be resolved. The caller tested the path first, so
// only a missing parent (or a race) gets here. The last name is never "." or
// "..": the path does not exist, and a path that ends in one of them names its
// parent, so a parent that resolves would make the path exist.
func wouldBeUnder(path string) (string, bool) {
	parent, last := splitLast(path)
	real, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", false
	}
	return filepath.ToSlash(filepath.Join(real, last)), true
}

// realpathFailure maps a failed call to the -32603 reply. The no_usable_name
// reason is not built, and Windows no_permission is not measured. An error with
// no errno gets reason other and errno 0 (claustrum's own, not measured).
func realpathFailure(id interface{}, err error) response {
	var en syscall.Errno
	if !errors.As(err, &en) {
		return errResultData(id, codeInternal, "files.realpath: "+err.Error(),
			realpathErrData{Reason: "other"})
	}
	reason := "other"
	switch {
	case isLinkLoopErrno(en):
		reason = "too_many_links"
	case errors.Is(err, fs.ErrPermission):
		reason = "no_permission"
	}
	return errResultData(id, codeInternal,
		"files.realpath: error "+strconv.Itoa(int(en))+": "+en.Error(),
		realpathErrData{Reason: reason, Errno: int(en)})
}
