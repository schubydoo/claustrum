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
	_, err := os.Stat(path)
	if err == nil {
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			return realpathResult{}, err
		}
		return realpathResult{Exists: true, Real: filepath.ToSlash(real)}, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return realpathResult{}, err
	}
	if !missingLeaf {
		return realpathResult{}, nil
	}
	if fi, lerr := os.Lstat(path); lerr == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return resolveDangling(path)
	}
	wouldBe, ok, err := wouldBeUnder(path)
	if err != nil || !ok {
		return realpathResult{}, err
	}
	return realpathResult{WouldBe: wouldBe}, nil
}

func resolveDangling(path string) (realpathResult, error) {
	res := realpathResult{Dangling: true}
	target, err := os.Readlink(path)
	if err != nil {
		return realpathResult{}, err
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	wouldBe, ok, err := wouldBeUnder(target)
	if err != nil {
		return realpathResult{}, err
	}
	if ok {
		res.WouldBe = wouldBe
	}
	return res, nil
}

// wouldBeUnder resolves the parent folder of path and adds the last name. ok is
// false when the parent does not exist.
func wouldBeUnder(path string) (string, bool, error) {
	parent := filepath.Dir(path)
	if _, err := os.Stat(parent); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	real, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", false, err
	}
	return filepath.ToSlash(filepath.Join(real, filepath.Base(path))), true, nil
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
