//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
)

// The Windows rules of the config read (gitrepoconfig.go). 5fd08069 answers
// these three texts on Windows (measured).

// openNoTerminal adds nothing on Windows, where an open gives the daemon no
// controlling terminal.
const openNoTerminal = 0

// configLinkDetail refuses a config file that is a symbolic link (measured with a file link made with
// mklink). A git directory that is a junction or a directory symbolic link has its own test
// (gitDirLinkDetail). Another link in a folder above the file is not measured, and claustrum does not
// test for it. An included file gets the same test (not measured).
func configLinkDetail(path string) string {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return quoteOperand(path) + configLinkTail
	}
	return ""
}

// includeValueDetail refuses an `include.path` value that is not relative: an
// absolute path, also to a file that is missing, and `~/x` (measured, also for
// the missing include). A value that starts with a slash or with a drive letter
// is not measured: claustrum refuses it the same way. `~root/x` gets this text
// too (measured on Windows). The value is cut to its first 300 characters, as a
// path is. A value of 300 is whole, and one of 301 shows 300 (measured on
// Windows).
func includeValueDetail(file, value string) string {
	if filepath.IsAbs(value) || filepath.VolumeName(value) != "" || strings.HasPrefix(value, "~") ||
		strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\`) {
		return quoteOperand(file) + " includes " + quoteOperand(value) +
			", and on Windows Claude follows an include only if its path is relative and stays inside the repository; " +
			"if that line is yours and you trust the file, put the line into ~/.gitconfig on the remote machine instead"
	}
	return ""
}

// includePlaceDetail refuses an included file that is outside the repository
// (measured with `../../outinc.cfg` in the config of <repo>\.git). The repository is
// the folder that holds the `.git` folder. A relative include inside `.git` passes
// (measured). For a git directory with another name the repository is that
// directory: a bare repository `bare.git` is measured so (measured on Windows). The
// test is on the text of the paths, and no link is resolved (not measured). The path
// in this text is the cleaned one (measured).
func includePlaceDetail(target, gitDir string) string {
	repo := gitDir
	if strings.EqualFold(filepath.Base(gitDir), ".git") {
		repo = filepath.Dir(gitDir)
	}
	rel, err := filepath.Rel(repo, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, `..\`) || filepath.IsAbs(rel) {
		return quoteOperand(filepath.Clean(target)) + " is outside " + quoteOperand(repo) +
			", and on Windows Claude reads a repository's settings only from inside it; " +
			"if the line that includes it is yours and you trust the file, put that line into ~/.gitconfig on the remote machine instead"
	}
	return ""
}
