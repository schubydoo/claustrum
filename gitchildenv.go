package main

import (
	"os"
	"strings"
)

// The cleaning of the daemon's own environment for the git children of the git
// methods. 5fd08069 cleans it so in rounds task004-01 and task004-02 (Linux,
// macOS and Windows VMs, a logging git wrapper). The cleaning acts on the
// daemon's own entries only. The names that claustrum adds or drops for a call
// stay as they were.
//
// The excludes read (resolveUserExcludesFile) and the `--attr-source` version
// probe (attrSourceArgs) keep the daemon's environment as it is. 5fd08069 passes
// every name as it is to them in both rounds. The plain `git version` calls get
// no cleaning (from the code, no round saw one).
//
// The rules of one entry:
//
//   - A list name (gitChildListNames) keeps the entries that are absolute and
//     hold no ';', no '"' and no newline. The entries keep their order and
//     their duplicates. With none left, the name goes. On Windows the list
//     separator is ';', the split follows the quote rule of that system, and
//     PATH matches in any letter case. Elsewhere the names match exactly.
//   - LD_PRELOAD and LD_AUDIT keep the pieces that are absolute or hold no '/'.
//     LD_PRELOAD splits on ':' and on a space, LD_AUDIT on ':' only. The kept
//     pieces are joined by ':'. With none left, the name goes.
//   - GIT_SSH stays when it is absolute or holds no '/' and no '\', so an empty
//     value stays. Else it goes.
//
// Absolute is a leading '/' on Linux and macOS. On Windows it is a drive letter,
// ':' and a slash. These states are not measured: on Windows a UNC path, the
// form C:/, a newline in an entry and the letter case of the other names. Nor
// is an empty piece of LD_PRELOAD or LD_AUDIT. claustrum drops an empty piece
// and a UNC path, and keeps C:/.

// gitChildListNames are the names whose value is a list of folders or files.
var gitChildListNames = []string{
	"PATH", "GIT_EXEC_PATH", "LD_LIBRARY_PATH", "DYLD_LIBRARY_PATH", "DYLD_FRAMEWORK_PATH",
	"DYLD_INSERT_LIBRARIES", "DYLD_FALLBACK_LIBRARY_PATH", "DYLD_FALLBACK_FRAMEWORK_PATH",
}

// gitChildEnv is the daemon's own environment as a git child of the git methods
// gets it, before the names that the call adds.
func gitChildEnv() []string {
	return cleanGitChildEnv(os.Environ(), gitEnvWindowsForm)
}

// cleanGitChildEnv applies the rules above to env. windows selects the rules of
// Windows. Every other entry stays as it is, in its place.
func cleanGitChildEnv(env []string, windows bool) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		var v string
		switch {
		case isGitChildListName(name, windows):
			v = cleanFolderList(value, windows)
		case name == "LD_PRELOAD":
			v = cleanLoaderList(value, true, windows)
		case name == "LD_AUDIT":
			v = cleanLoaderList(value, false, windows)
		case name == "GIT_SSH":
			if !absoluteFor(value, windows) && strings.ContainsAny(value, `/\`) {
				continue
			}
			out = append(out, kv)
			continue
		default:
			out = append(out, kv)
			continue
		}
		if v != "" {
			out = append(out, name+"="+v)
		}
	}
	return out
}

// isGitChildListName reports whether name is a list name. On Windows PATH
// matches in any letter case.
func isGitChildListName(name string, windows bool) bool {
	if windows && strings.EqualFold(name, "PATH") {
		return true
	}
	for _, n := range gitChildListNames {
		if name == n {
			return true
		}
	}
	return false
}

// cleanFolderList keeps the absolute entries of a list value that hold no ';',
// no '"' and no newline, joined by the list separator. "" means that no entry
// is left.
func cleanFolderList(value string, windows bool) string {
	sep := ":"
	if windows {
		sep = ";"
	}
	var kept []string
	for _, e := range splitFolderList(value, windows) {
		if absoluteFor(e, windows) && !strings.ContainsAny(e, ";\"\n") {
			kept = append(kept, e)
		}
	}
	return strings.Join(kept, sep)
}

// splitFolderList splits a list value. On Windows a ';' between two '"' does not
// split, and every '"' goes from the entries. That is the rule of
// filepath.SplitList on Windows, written here so that every system runs it.
func splitFolderList(value string, windows bool) []string {
	if value == "" {
		return nil
	}
	if !windows {
		return strings.Split(value, ":")
	}
	var list []string
	start, quoted := 0, false
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '"':
			quoted = !quoted
		case ';':
			if !quoted {
				list = append(list, value[start:i])
				start = i + 1
			}
		}
	}
	list = append(list, value[start:])
	for i, e := range list {
		list[i] = strings.ReplaceAll(e, `"`, "")
	}
	return list
}

// cleanLoaderList keeps the pieces of an LD_PRELOAD or LD_AUDIT value that are
// absolute or hold no '/', joined by ':'. space makes a space split too.
func cleanLoaderList(value string, space, windows bool) string {
	pieces := strings.FieldsFunc(value, func(r rune) bool {
		return r == ':' || (space && r == ' ')
	})
	var kept []string
	for _, p := range pieces {
		if absoluteFor(p, windows) || !strings.Contains(p, "/") {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, ":")
}

// absoluteFor reports whether p is absolute by the rule of the cleaning.
func absoluteFor(p string, windows bool) bool {
	if !windows {
		return strings.HasPrefix(p, "/")
	}
	if len(p) < 3 || p[1] != ':' || (p[2] != '\\' && p[2] != '/') {
		return false
	}
	c := p[0] | 0x20
	return c >= 'a' && c <= 'z'
}

// withoutGitNames drops every entry whose name starts with GIT_. The calls of the
// start test get the daemon's environment so: 5fd08069 passes none of the
// daemon's GIT_ names to those nine calls (rounds task004-01 and task004-02,
// Linux, macOS and Windows VMs).
func withoutGitNames(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, "GIT_") {
			out = append(out, kv)
		}
	}
	return out
}
