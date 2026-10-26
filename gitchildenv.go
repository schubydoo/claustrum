package main

import (
	"os"
	"slices"
	"strings"
	"unicode/utf8"
)

// The cleaning of the daemon's own environment for the git children of the git
// methods. The rules are claustrum's own, not measured.
// They replace the older filter of 5fd08069 (rounds task004-01
// and task004-02). The cleaning acts on the daemon's own entries only. The names
// that claustrum adds or drops for a call stay as they were.
//
// The calls outside a repository (gitoutsidecalls.go) start from this cleaned
// environment too: the excludes read, the `--attr-source` probe, the version gate,
// the `git version` run and the read of config text.
//
// The tests run in this order for one entry, and the first one that drops or
// keeps the entry ends the work:
//
//   - An entry with no '=' goes. On Windows the name is upper-cased for every
//     test, and a kept or rebuilt entry keeps the spelling of its name.
//   - The pathspec names (gitChildPathspecNames) go.
//   - On Windows GIT_CONFIG_GLOBAL and GIT_CONFIG_SYSTEM stay at once with the
//     exact value /dev/null.
//   - A relative value (relativeFor) drops the names of gitChildRelativeNames.
//   - A Kerberos name drops when the file part of its value is relative.
//   - On Windows an empty HOME, USERPROFILE, PROGRAMDATA or ALLUSERSPROFILE goes,
//     and HOMEDRIVE and HOMEPATH have their own tests. A relative
//     SSH_AUTH_SOCK goes, on Windows unless it starts with a slash.
//   - An empty value stays for the names of gitChildEmptyKept.
//   - A list name (gitChildListNames) keeps the entries that are absolute and
//     hold no ';', no '"' and no newline. The entries keep their order and
//     their duplicates. With none left, the name goes. On Windows the list
//     separator is ';' and the split follows the quote rule of that system.
//   - LD_PRELOAD and LD_AUDIT keep the pieces that are absolute or hold no '/'.
//     LD_PRELOAD splits on ':' and on white space, LD_AUDIT on ':' only. The
//     kept pieces are joined by ':'. With none left, the name goes.
//   - A program name (gitChildProgramNames) goes when it is not absolute and
//     holds a slash or a backslash, and on Windows when it has a volume name.
//   - SSH_SK_PROVIDER also needs the value internal or an absolute path on macOS
//     and Windows.
//
// Absolute is a leading '/' on Linux and macOS. On Windows it is a drive letter,
// ':' and a slash, or a UNC path. The exact shape of a UNC path, an empty name on
// Windows, a type of one multi-byte character in a Kerberos value and a value of
// HOMEDRIVE with a volume name only in part are not measured.

// gitEnvForm selects the rules of one system. Linux is the zero value.
type gitEnvForm struct{ windows, darwin bool }

// gitEnvHere is the form of the system that the daemon runs on.
var gitEnvHere = gitEnvForm{windows: gitEnvWindowsForm, darwin: gitEnvDarwinForm}

// gitChildPathspecNames are the names that always go.
var gitChildPathspecNames = []string{
	"GIT_LITERAL_PATHSPECS", "GIT_GLOB_PATHSPECS", "GIT_NOGLOB_PATHSPECS", "GIT_ICASE_PATHSPECS",
}

// gitChildRelativeNames are the names that go when their value is relative.
var gitChildRelativeNames = []string{
	"HOME", "USERPROFILE", "PROGRAMDATA", "ALLUSERSPROFILE", "XDG_CACHE_HOME", "XDG_CONFIG_HOME",
	"KRB5_TRACE", "KRB5RCACHEDIR", "GSS_MECH_CONFIG",
	"OPENSSL_CONF", "OPENSSL_CONF_INCLUDE", "OPENSSL_ENGINES", "OPENSSL_MODULES",
	"SSLKEYLOGFILE", "SSL_CERT_FILE", "CURL_CA_BUNDLE",
	"GIT_INDEX_FILE", "GIT_SSL_CAINFO", "GIT_PROXY_SSL_CAINFO", "GIT_TEMPLATE_DIR",
	"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_TEXTDOMAINDIR",
}

// gitChildKerberosNames are the names whose value holds a file part.
var gitChildKerberosNames = []string{"KRB5CCNAME", "KRB5_KTNAME", "KRB5_CLIENT_KTNAME"}

// gitChildHomeNames are the names that go on Windows when their value is empty.
var gitChildHomeNames = []string{"HOME", "USERPROFILE", "PROGRAMDATA", "ALLUSERSPROFILE"}

// gitChildEmptyKept are the list names whose empty value stays.
var gitChildEmptyKept = []string{
	"SSL_CERT_DIR", "GIT_SSL_CAPATH", "KRB5_CONFIG", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "SASL_PATH",
}

// gitChildListNames are the names whose value is a list of folders or files.
var gitChildListNames = []string{
	"PATH", "LOCPATH", "NLSPATH", "GCONV_PATH", "LD_LIBRARY_PATH",
	"SASL_PATH", "SASL_CONF_PATH", "KRB5_CONFIG", "SSL_CERT_DIR",
	"GIT_EXEC_PATH", "GIT_SSL_CAPATH", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"DYLD_LIBRARY_PATH", "DYLD_FRAMEWORK_PATH", "DYLD_INSERT_LIBRARIES",
	"DYLD_FALLBACK_LIBRARY_PATH", "DYLD_FALLBACK_FRAMEWORK_PATH",
	"DYLD_VERSIONED_LIBRARY_PATH", "DYLD_VERSIONED_FRAMEWORK_PATH",
}

// gitChildProgramNames are the names whose value is a program.
var gitChildProgramNames = []string{
	"GIT_SSH", "GIT_ASKPASS", "SSH_ASKPASS", "SSH_SK_HELPER", "SSH_SK_PROVIDER", "SSH_PKCS11_HELPER",
}

// gitChildEnv is the daemon's own environment as a git child of the git methods
// gets it, before the names that the call adds.
func gitChildEnv() []string {
	return cleanGitChildEnv(os.Environ(), gitEnvHere)
}

// cleanGitChildEnv applies the rules above to env. form selects the rules of one
// system. Every other entry stays as it is, in its place.
func cleanGitChildEnv(env []string, form gitEnvForm) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		key := name
		if form.windows {
			key = strings.ToUpper(name)
		}
		if v, keep := cleanEntry(key, value, form); keep {
			out = append(out, name+"="+v)
		}
	}
	return out
}

// cleanEntry runs the tests on one entry. key is the name as the rules match it.
// It returns the value to write and false when the entry goes.
func cleanEntry(key, value string, f gitEnvForm) (string, bool) {
	windows := f.windows
	if slices.Contains(gitChildPathspecNames, key) {
		return "", false
	}
	if windows && (key == "GIT_CONFIG_GLOBAL" || key == "GIT_CONFIG_SYSTEM") && value == "/dev/null" {
		return value, true
	}
	if relativeFor(value, windows) && slices.Contains(gitChildRelativeNames, key) {
		return "", false
	}
	if slices.Contains(gitChildKerberosNames, key) && relativeFor(kerberosFilePart(value), windows) {
		return "", false
	}
	if windows && value == "" && slices.Contains(gitChildHomeNames, key) {
		return "", false
	}
	switch {
	case key == "SSH_AUTH_SOCK":
		if relativeFor(value, windows) && (!windows || !strings.ContainsAny(value[:1], `\/`)) {
			return "", false
		}
	case windows && key == "HOMEDRIVE":
		if value == "" || windowsVolumeLen(value) != len(value) {
			return "", false
		}
	case windows && key == "HOMEPATH":
		if value == "" || !strings.ContainsAny(value[:1], `\/`) {
			return "", false
		}
	}
	if value == "" && slices.Contains(gitChildEmptyKept, key) {
		return value, true
	}
	var v string
	switch {
	case slices.Contains(gitChildListNames, key):
		v = cleanFolderList(value, windows)
	case key == "LD_PRELOAD":
		v = cleanLoaderList(value, true, windows)
	case key == "LD_AUDIT":
		v = cleanLoaderList(value, false, windows)
	default:
		if slices.Contains(gitChildProgramNames, key) && badProgram(value, windows) {
			return "", false
		}
		if key == "SSH_SK_PROVIDER" && (f.darwin || windows) &&
			(value == "" || value != "internal" && !absoluteFor(value, windows)) {
			return "", false
		}
		return value, true
	}
	return v, v != ""
}

// badProgram reports whether a program value is a path that is not absolute: it
// holds a slash or a backslash, and on Windows a volume name counts too. A bare
// name and an empty value pass.
func badProgram(value string, windows bool) bool {
	if absoluteFor(value, windows) {
		return false
	}
	return strings.ContainsAny(value, `/\`) || windows && windowsVolumeLen(value) > 0
}

// relativeFor reports whether value is relative by the rules of the cleaning. An
// empty value is not. On Windows NUL in any letter case is not either, and a
// value with a leading slash is.
func relativeFor(value string, windows bool) bool {
	if value == "" {
		return false
	}
	if windows {
		return !absoluteFor(value, true) && !strings.EqualFold(value, "NUL")
	}
	return value[0] != '/'
}

// kerberosFilePart is the file part of a KRB5CCNAME, KRB5_KTNAME or
// KRB5_CLIENT_KTNAME value, "" for a type that names no file. The type is the text
// before the first ':'. A one-character type is a drive letter and a type with a
// slash or a backslash is a path, so the whole value is the file part then.
func kerberosFilePart(value string) string {
	typ, rest, found := strings.Cut(value, ":")
	if !found || utf8.RuneCountInString(typ) == 1 || strings.ContainsAny(typ, `/\`) {
		return value
	}
	switch strings.ToUpper(typ) {
	case "KEYRING", "KCM", "API", "MEMORY", "XCACHE", "XCTEMP":
		return ""
	case "DIR":
		return strings.TrimPrefix(rest, ":")
	}
	return rest
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
// absolute or hold no '/', joined by ':'. space makes white space split too.
func cleanLoaderList(value string, space, windows bool) string {
	pieces := strings.FieldsFunc(value, func(r rune) bool {
		return r == ':' || (space && strings.ContainsRune(" \t\n\v\f\r", r))
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
	if isUNC(p) {
		return true
	}
	if len(p) < 3 || p[1] != ':' || (p[2] != '\\' && p[2] != '/') {
		return false
	}
	c := p[0] | 0x20
	return c >= 'a' && c <= 'z'
}

// isUNC reports whether p starts with two slashes and then a name (not measured
// beyond that shape).
func isUNC(p string) bool {
	return len(p) > 2 && (p[0] == '\\' || p[0] == '/') && (p[1] == '\\' || p[1] == '/') &&
		p[2] != '\\' && p[2] != '/'
}

// windowsVolumeLen is the length of the volume name at the start of p: a drive
// letter and ':', or the server and the share of a UNC path. It is 0 with none.
func windowsVolumeLen(p string) int {
	if len(p) >= 2 && p[1] == ':' && (p[0]|0x20) >= 'a' && (p[0]|0x20) <= 'z' {
		return 2
	}
	if !isUNC(p) {
		return 0
	}
	server := strings.IndexAny(p[2:], `/\`)
	if server < 0 {
		return len(p)
	}
	share := strings.IndexAny(p[3+server:], `/\`)
	if share < 0 {
		return len(p)
	}
	return 3 + server + share
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
