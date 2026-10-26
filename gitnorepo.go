package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The "no repository" verdict of a failed configuration listing (claustrum's own,
// not measured). The text of git's message plays no part. The daemon looks for a git dir
// itself, upward from the folder of the listing, and judges it.

// listingFindsNoRepository is the verdict of listingRun.saysNoRepository for the folder
// dir: true when the search finds no git dir, or a git dir that git cannot use.
func listingFindsNoRepository(dir string) bool {
	g, ok := discoverGitDir(dir)
	if !ok {
		return true
	}
	if isGitDirShape(g) {
		return false
	}
	if !headLooksValid(filepath.Join(g, "HEAD")) {
		return true
	}
	// A linked worktree's git dir keeps a commondir. It is not followed.
	_, err := os.Lstat(filepath.Join(g, "commondir"))
	return err != nil
}

// discoverGitDir looks for a git dir upward from dir. It reads the
// environment of the daemon, not that of the call. It differs from findGitDir in four
// ways: it stops at the device change (Linux and macOS) and at the ceilings, it tests a
// folder with a `.git` of another type as a bare repository, and it starts from the
// path with symlinks resolved only (no walkStartsAsSpelled). A `.git` file is read by
// readGitFile, which wants `gitdir:` and not `gitdir: ` (not measured).
func discoverGitDir(dir string) (string, bool) {
	start, err := filepath.EvalSymlinks(dir)
	if err == nil {
		start, err = filepath.Abs(start)
	} else if start, err = filepath.Abs(dir); err == nil {
		if r, rerr := filepath.EvalSymlinks(start); rerr == nil {
			start = r
		}
	}
	if err != nil {
		return "", false
	}
	across := discoveryAcrossFilesystem()
	ceilings := discoveryCeilings()
	for d := start; ; {
		if !across && d != start && otherDevice(start, d) {
			return "", false
		}
		dotGit := filepath.Join(d, ".git")
		fi, err := os.Stat(dotGit)
		switch {
		case err == nil && fi.IsDir():
			if isGitDirShape(dotGit) {
				return dotGit, true
			}
			if _, err := os.Lstat(filepath.Join(dotGit, "commondir")); err == nil {
				return dotGit, true
			}
			return "", false
		case err == nil && fi.Mode().IsRegular():
			return readGitFile(dotGit, d)
		case isGitDirShape(d):
			return d, true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", false
		}
		for _, c := range ceilings {
			if sameGitDirPath(parent, c) {
				return "", false
			}
		}
		d = parent
	}
}

// discoveryAcrossFilesystem reports a git true value in GIT_DISCOVERY_ACROSS_FILESYSTEM
// of the daemon: a word of gitTrue or a non-zero integer.
func discoveryAcrossFilesystem() bool {
	v := os.Getenv("GIT_DISCOVERY_ACROSS_FILESYSTEM")
	if n, err := strconv.Atoi(v); err == nil {
		return n != 0
	}
	return gitTrue(v)
}

// discoveryCeilings reads GIT_CEILING_DIRECTORIES of the daemon. Only absolute entries
// count. Each is resolved through symlinks. After an empty entry the later entries are
// only cleaned. An entry whose resolution fails is only cleaned.
func discoveryCeilings() []string {
	var out []string
	resolve := true
	for _, e := range filepath.SplitList(os.Getenv("GIT_CEILING_DIRECTORIES")) {
		if e == "" {
			resolve = false
			continue
		}
		if !filepath.IsAbs(e) {
			continue
		}
		if resolve {
			if r, err := filepath.EvalSymlinks(e); err == nil {
				e = r
			}
		}
		out = append(out, filepath.Clean(e))
	}
	return out
}

// listingNamesGitDir reports a `--git-dir=` option or a `GIT_DIR=` entry. env is
// the environment of the call, or the daemon's when nil.
func listingNamesGitDir(gitDirOption string, env []string) bool {
	if gitDirOption != "" {
		return true
	}
	if env == nil {
		env = os.Environ()
	}
	for _, kv := range env {
		// A byte compare with exact case, also on Windows.
		if strings.HasPrefix(kv, "GIT_DIR=") {
			return true
		}
	}
	return false
}
