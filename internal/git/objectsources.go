package git

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/schubydoo/claustrum/internal/logging"
)

// The checks of the object sources of a repository, and of a chain of links at
// baseRepo. b3081b51 runs them on Windows only. On Linux and macOS the same file
// shapes and the same environment give no refusal (measured). Each check reads
// files only, and none starts git.

// objectSourceChecks turns the checks on. It is true on Windows. The tests set
// it on every system.
var objectSourceChecks = gitEnvWindowsForm

const (
	// alternatesMaxBytes is the largest alternates file that the check reads.
	alternatesMaxBytes = 1 << 20
	// alternatesMaxDirs is the most object directories that the check counts.
	alternatesMaxDirs = 64
	// alternatesMaxDepth is the deepest level of a listed directory. The
	// repository's own file is level 0.
	alternatesMaxDepth = 6
	// linkChainCap is the link of a chain that is refused: 31 links pass and
	// the 32nd is refused (measured on Windows).
	linkChainCap = 32
)

// alternatesRepack is the end of three texts of the alternates check.
const alternatesRepack = ` To stop depending on that file, run "git repack -a -d" in the repository it belongs to, then delete that file.`

func alternatesCRLFText(file string) string {
	return "`" + file + "` has Windows line endings, which git reads as part of each path, so git wasn't run. Save that file with Unix line endings."
}

func alternatesQuoteText(file string) string {
	return "`" + file + "` has a quoted line, which Claude doesn't support, so git wasn't run." + alternatesRepack
}

func alternatesControlText(file string) string {
	return "`" + file + "` has a line with a control character, which git takes as part of the path, so git wasn't run." + alternatesRepack
}

func alternatesDotDotText(file, link string) string {
	return "`" + file + "` holds a path in which \"..\" comes after a symlink or junction (`" + link +
		"`), which Windows and git resolve differently, so git wasn't run. Rewrite that path as a full path without \"..\"."
}

func alternatesUnreadableText(file string) string {
	return "`" + file + "` couldn't be read as a regular file of at most 1 MiB, so git wasn't run. Check that file and its permissions."
}

func alternatesTooManyText(file string) string {
	return "`" + file + "` lists more object directories than Claude checks (at most 64 in total, nested at most 6 deep), so git wasn't run." + alternatesRepack
}

// alternateEnvText is the refusal of GIT_ALTERNATE_OBJECT_DIRECTORIES in the
// daemon's environment (measured on Windows).
const alternateEnvText = "The remote machine's environment sets GIT_ALTERNATE_OBJECT_DIRECTORIES, which Claude can't use on a Windows host, so git wasn't run. Remove it from the environment, then restart the machine."

// objectDirLine is the log line of GIT_OBJECT_DIRECTORY in the daemon's
// environment (measured on Windows).
const objectDirLine = "[GitHandler] GIT_OBJECT_DIRECTORY from the helper's environment is ignored: on a Windows host the helper finds the repository from the folder that the app names"

// linkChainText names path cleaned, so a forward slash reads as a backslash on
// Windows (measured on Windows).
func linkChainText(path string) string {
	return "Claude couldn't check that `" + filepath.Clean(path) + "` stays on a local drive (too many links, or a loop of links), so git wasn't run. If a folder name in that path ends in a dot or a space, rename it. Otherwise, replace the link in that path with the folder it points to."
}

// objectDirLogged is true once the daemon logged objectDirLine. One line per
// daemon is claustrum's own, not measured: the row has one request.
var objectDirLogged atomic.Bool

// statusSourceRefusal is the refusal of git.status before its first git call
// after the excludes read, or "". common is the git directory that the trust
// check accepted. b3081b51 answers each text there (measured on Windows). The
// order of the checks and their place after the trust check are claustrum's own,
// not measured.
func statusSourceRefusal(common string) string {
	if !objectSourceChecks {
		return ""
	}
	if os.Getenv("GIT_OBJECT_DIRECTORY") != "" && objectDirLogged.CompareAndSwap(false, true) {
		logging.Infof(objectDirLine)
	}
	// An empty value is no source of objects (claustrum's own, not measured).
	if os.Getenv("GIT_ALTERNATE_OBJECT_DIRECTORIES") != "" {
		return alternateEnvText
	}
	return alternatesRefusal(common, true)
}

// statusLinkChainRefusal is the refusal of a baseRepo that resolves through
// linkChainCap or more links, or "". b3081b51 answers it for chains of 32 to 70
// links, also where the system cannot resolve the path (measured on Windows). Its
// place before the other tests of git.status is claustrum's own, not measured.
// So is the test of baseRepo alone: the rows send one chain as path and baseRepo.
func statusLinkChainRefusal(baseRepo string) string {
	if objectSourceChecks && linkChainTooLong(baseRepo) {
		return linkChainText(baseRepo)
	}
	return ""
}

// alternatesRefusal checks `<common>/objects/info/alternates` and the files of
// the directories that it lists, and returns a refusal text or "". common is
// the git directory of the repository, or "" for none.
//
// all is true for git.status, which runs every test. git.info,
// git.worktree_create and git.worktree_remove run only the tests of Windows
// line endings and of the count, the two that the rows measure there.
//
// Measured on Windows: a file with CRLF, a line that starts with a quote, a
// tab, a ".." after a link, a file of 1048577 bytes or a folder, 1000 lines,
// and chains of 20 and 22 directories. A chain names the file of level 6. The
// order of the tests, a relative line, a comment line, the bound of exactly 64
// and a listed directory that is gone are claustrum's own, not measured.
func alternatesRefusal(common string, all bool) string {
	if !objectSourceChecks || common == "" {
		return ""
	}
	dirs := 0
	var scan func(objects string, depth int) string
	scan = func(objects string, depth int) string {
		file := filepath.Join(objects, "info", "alternates")
		data, err := readAlternates(file)
		if errors.Is(err, fs.ErrNotExist) {
			return ""
		}
		if err != nil {
			if all {
				return alternatesUnreadableText(file)
			}
			return ""
		}
		if bytes.Contains(data, []byte("\r\n")) {
			return alternatesCRLFText(file)
		}
		var listed []string
		for _, line := range strings.Split(string(data), "\n") {
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if all {
				if strings.HasPrefix(line, `"`) {
					return alternatesQuoteText(file)
				}
				if hasControlByte(line) {
					return alternatesControlText(file)
				}
				if link := linkBeforeDotDot(objects, line); link != "" {
					return alternatesDotDotText(file, link)
				}
			}
			if dirs++; depth >= alternatesMaxDepth || dirs > alternatesMaxDirs {
				return alternatesTooManyText(file)
			}
			listed = append(listed, alternateDir(objects, line))
		}
		for _, dir := range listed {
			if msg := scan(dir, depth+1); msg != "" {
				return msg
			}
		}
		return ""
	}
	return scan(filepath.Join(common, "objects"), 0)
}

// readAlternates reads a regular file of at most alternatesMaxBytes. Any other
// entry is an error. A file that is not there is fs.ErrNotExist.
func readAlternates(file string) ([]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, errNotRegularFile
	}
	data, err := io.ReadAll(io.LimitReader(f, alternatesMaxBytes+1))
	if err == nil && len(data) > alternatesMaxBytes {
		err = errNotRegularFile
	}
	return data, err
}

func isPathSep(r rune) bool { return r < 0x80 && os.IsPathSeparator(uint8(r)) }

// alternateDir is the directory that a line names. A relative line is read
// from the objects directory of its file, as git reads it.
func alternateDir(objects, line string) string {
	p := filepath.FromSlash(line)
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(objects, p)
}

// linkBeforeDotDot returns the first name before the first ".." of line that is
// a symbolic link or a junction, as a path, or "". Go reports a junction as an
// irregular file. A relative line starts at objects, and only its own names are
// tested (claustrum's own, not measured).
func linkBeforeDotDot(objects, line string) string {
	p := filepath.FromSlash(line)
	cur := ""
	if filepath.IsAbs(p) {
		vol := filepath.VolumeName(p)
		cur, p = vol+string(filepath.Separator), p[len(vol):]
	} else {
		cur = objects
	}
	names := strings.FieldsFunc(p, isPathSep)
	end := slices.Index(names, "..")
	if end < 0 {
		return ""
	}
	for _, name := range names[:end] {
		next := filepath.Join(cur, name)
		if name != "." {
			if fi, err := os.Lstat(next); err == nil && fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
				return next
			}
		}
		cur = next
	}
	return ""
}

// linkChainTooLong reports whether the path resolves through linkChainCap or more
// symbolic links. Each link is followed by its target text, as the system
// reads it. A junction is not counted, and a name that cannot be read ends the
// test with false: the request goes on as before (claustrum's own, not
// measured).
func linkChainTooLong(path string) bool {
	p, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for links := 0; ; {
		vol := filepath.VolumeName(p)
		names := strings.FieldsFunc(p[len(vol):], isPathSep)
		cur := vol + string(filepath.Separator)
		next := ""
		for i, name := range names {
			at := filepath.Join(cur, name)
			fi, err := os.Lstat(at)
			if err != nil {
				return false
			}
			if fi.Mode()&fs.ModeSymlink == 0 {
				cur = at
				continue
			}
			if links++; links >= linkChainCap {
				return true
			}
			target, err := os.Readlink(at)
			if err != nil {
				return false
			}
			switch {
			case filepath.IsAbs(target):
			case target != "" && os.IsPathSeparator(target[0]):
				target = filepath.VolumeName(cur) + target
			default:
				target = filepath.Join(cur, target)
			}
			next = filepath.Join(append([]string{target}, names[i+1:]...)...)
			break
		}
		if next == "" {
			return false
		}
		p = next
	}
}
