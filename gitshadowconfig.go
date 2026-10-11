package main

import (
	"strconv"
	"strings"
)

// The text of the settings file that the daemon keeps for one repository, as
// 5fd08069 writes it. The file is a list of blocks. Each carried setting is one
// block of two lines, with a new header before every key and the value in double
// quotes. The blocks are in the order of the repository's file. The file ends
// with one fixed block.
//
// Only a setting that a VM cell showed as carried is written. A key, a section
// or a class of characters that no cell covers is left out: git then reads only
// what the cells show. testdata/shadow_config_cells.json has the fixture and the
// text of each cell, and TestShadowConfigCells runs them. The rounds are s3p2,
// s3p2b-edges and s3p2b-edges2 on a Linux VM with git 2.43.0.

// repoSetting is one setting of a config file, as `git config --list -z` gives
// it. key has the section and the name in lower case, and the subsection as
// written. included is true for a setting of a file that an include names.
type repoSetting struct {
	key      string
	value    string
	hasValue bool
	included bool
}

// shadowConfigTail ends every copy. Its value has no quotes (s3-linux cell A3,
// and cell B3c of s3p2-linux for a bare repository).
const shadowConfigTail = "[lfs]\n\trepositoryformatversion = 0\n"

// shadowBoolKeys are the settings that are written as "true" or "false". Each
// name is one that a cell showed as carried: cells A1, A2 and A3 of s3p2-linux,
// cells 04d to 04n, 05b and F6 of s3p2b-edges-linux, cell A5-promisor, and for
// the two macOS names s3-macos cell A3. The four extension names ran with the
// value `true` only (cells E9 and 13b to 13k). `extensions.noop-v1 = true` is
// carried as "true" with format version 0 and 1, in both letter cases of the
// key (cells X8 of val-s3p2b2-3-linux2, Linux VM).
var shadowBoolKeys = map[string]bool{
	"core.filemode": true, "core.bare": true, "core.logallrefupdates": true,
	"core.ignorecase": true, "core.symlinks": true, "core.precomposeunicode": true,
	"core.quotepath": true, "core.untrackedcache": true, "core.splitindex": true,
	"core.longpaths": true, "core.protectntfs": true, "core.sparsecheckout": true,
	"feature.manyfiles": true,
	"extensions.noop":   true, "extensions.noop-v1": true, "extensions.preciousobjects": true,
	"extensions.relativeworktrees": true, "extensions.submodulepathconfig": true,
}

// shadowWordKeys are the settings with a fixed set of carried values. Every
// value here ran in a cell: A1 and A3 of s3p2-linux, 04k, 13c, 13e and F6 of
// s3p2b-edges-linux, and E9 of s3p2-linux. Another value of these keys is not
// measured and is left out, such as `core.eol = crlf` and `index.version = 2`.
// `extensions.objectformat = sha1` is carried with format version 0 and 1 (cells
// X8 of val-s3p2b2-3-linux2, Linux VM).
var shadowWordKeys = map[string][]string{
	"core.eol":                      {"lf"},
	"core.sharedrepository":         {"group", "0660"},
	"index.version":                 {"3", "4"},
	"extensions.objectformat":       {"sha256", "sha1"},
	"extensions.compatobjectformat": {"sha256"},
	"extensions.refstorage":         {"files", "reftable"},
}

// shadowBoolOrWord are the settings that take a boolean or one more word:
// `core.autocrlf = input` (cell A1) and `= TRUE` (cell F6), `core.safecrlf =
// true` (cell 04e) and `= warn` (cell F6).
var shadowBoolOrWord = map[string]string{
	"core.autocrlf": "input",
	"core.safecrlf": "warn",
}

// shadowBool is the written form of a boolean value, or false for a value that
// is left out. `yes`, `on`, `1`, `True`, `2` and a key with no value give
// "true". `no`, `0`, `off`, `FALSE` and an empty value give "false". `maybe` is
// left out (cells 03a to 03m of s3p2b-edges-linux). A number is plain decimal
// digits here: a sign and a unit letter are not measured.
func shadowBool(s repoSetting) (string, bool) {
	if !s.hasValue {
		return "true", true
	}
	switch strings.ToLower(s.value) {
	case "true", "yes", "on":
		return "true", true
	case "false", "no", "off", "":
		return "false", true
	}
	if len(s.value) > 9 || strings.Trim(s.value, "0123456789") != "" {
		return "", false
	}
	if n, _ := strconv.Atoi(s.value); n != 0 {
		return "true", true
	}
	return "false", true
}

func asciiLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func asciiAlnum(c byte) bool {
	return asciiLetter(c) || (c >= '0' && c <= '9')
}

// shadowRemoteName reports whether name is carried as the name of a remote, as
// the value of branch.<name>.remote and as the value of remote.lfsdefault. The
// rule is fitted to cells F1, F2, F3 and F6 of s3p2b-edges-linux. Carried there:
// letters and digits with `-`, `_`, `.`, `/`, `+` and `@` after a first letter.
// Left out: 28 texts, among them a first `-` or `.`, a space, a quote, a
// backslash, `:`, `,`, `~`, `%`, `;`, `=`, `#` and a letter that is not ASCII.
// No carried name of a cell starts with another character than a letter, and
// none has two dots in a row or a part that starts or ends with a dot or a
// slash. Those forms are not measured and are left out.
func shadowRemoteName(name string) bool {
	if name == "" || !asciiLetter(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if c := name[i]; !asciiAlnum(c) && !strings.ContainsRune("-_./+@", rune(c)) {
			return false
		}
	}
	last := name[len(name)-1]
	return last != '.' && last != '/' &&
		!strings.Contains(name, "..") && !strings.Contains(name, "/.") &&
		!strings.Contains(name, "./") && !strings.Contains(name, "//")
}

// shadowBranchChars are the ASCII characters, beside letters and digits, that a
// carried branch name of cell F4 holds.
const shadowBranchChars = " -_./+@:,~%;=\"\\#*?[^!'$&|<{(`"

// shadowBranchName reports whether name is carried as the name of a branch.
// Cell F4 of s3p2b-edges-linux carried 38 of 39 texts and left out `-org` only.
// A byte of a character that is not ASCII is carried (`örg`). An ASCII
// character that no text of the cell holds is not measured and is left out: a
// control character, `)`, `]`, `}` and `>`.
func shadowBranchName(name string) bool {
	if name == "" || name[0] == '-' {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c < 0x80 && !asciiAlnum(c) && !strings.ContainsRune(shadowBranchChars, rune(c)) {
			return false
		}
	}
	return true
}

// shadowURLBytes reports whether no byte of u drops a URL value. A tab, a
// space, a newline, CR and the bytes 0x01, 0x08, 0x0c, 0x1f and 0x7f drop it
// (cells 01a, 01i, 02c, 02e, 02f and F1 of s3p2b-edges-linux). The other
// control bytes are not measured and drop it too. Byte 0xff and 30 more texts
// of cell F1 are carried.
func shadowURLBytes(u string) bool {
	for i := 0; i < len(u); i++ {
		if u[i] <= 0x20 || u[i] == 0x7f {
			return false
		}
	}
	return true
}

// shadowURL reports whether the remote URL u is carried. The carried forms are
// those of cells A5 of s3p2-linux and cells 7 and 8 of s3p2b-edges-linux:
// `https://`, `http://`, `ssh://`, `git://`, and the form `<host>:<path>` as in
// `git@example.invalid:o/r` and `gh:o/r.git`. Left out there: `file://`, a
// local path, `ftp://`, `../x`, an empty value, `ext::x` and `ext::sh -c x`.
// On a Windows VM `C:\x\r.git`, `C:/x/r.git`, `\\server\share\r.git` and
// `file:///C:/x/r.git` are left out (cells 7a to 7d of s3p2b-edges2-windows).
//
// The rule for `<host>:<path>` is fitted to those rows: a host of two or more
// letters, digits, `@`, `.`, `_` and `-` that starts with a letter or a digit,
// then one colon, then a path. A host of one letter is the Windows drive form.
// It is left out on every system: Linux and macOS did not run it. A first `-`
// after the scheme is not measured and is left out.
func shadowURL(u string) bool {
	if u == "" || !shadowURLBytes(u) {
		return false
	}
	for _, scheme := range []string{"https://", "http://", "ssh://", "git://"} {
		if rest, ok := strings.CutPrefix(u, scheme); ok {
			return rest != "" && rest[0] != '-'
		}
	}
	host, path, ok := strings.Cut(u, ":")
	if !ok || len(host) < 2 || path == "" || path[0] == ':' || strings.Contains(u, "://") {
		return false
	}
	if !asciiAlnum(host[0]) {
		return false
	}
	for i := 1; i < len(host); i++ {
		if c := host[i]; !asciiAlnum(c) && !strings.ContainsRune("@._-", rune(c)) {
			return false
		}
	}
	return true
}

// shadowLFSPattern reports whether v is carried as the value of
// lfs.fetchinclude and lfs.fetchexclude. Carried: `*.bin`, `*.tmp` and `a b`.
// Left out: a value with a tab (cells A6 of s3p2-linux, F6 of
// s3p2b-edges-linux and 6a to 6k of s3p2b-edges2-linux). Every other character
// is not measured and is left out, also `,` and `/`.
func shadowLFSPattern(v string) bool {
	if v == "" {
		return false
	}
	for i := 0; i < len(v); i++ {
		if c := v[i]; !asciiAlnum(c) && c != '*' && c != '.' && c != ' ' {
			return false
		}
	}
	return true
}

// shadowQuote writes s between double quotes. A quote is written `\"` and a
// backslash `\\` (cells 02a, 02b and F4 of s3p2b-edges-linux). No other
// escaped form was seen: no carried value holds a character that needs one.
func shadowQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// shadowBlock is one block of the copy.
func shadowBlock(section, subsection string, hasSub bool, name, value string) string {
	head := "[" + section
	if hasSub {
		head += " " + shadowQuote(subsection)
	}
	return head + "]\n\t" + name + " = " + shadowQuote(value) + "\n"
}

// splitSettingKey cuts a listed key into its section, its subsection and its
// name. The name has no dot, and the section has none.
func splitSettingKey(key string) (section, subsection string, hasSub bool, name string) {
	first := strings.IndexByte(key, '.')
	last := strings.LastIndexByte(key, '.')
	if first < 0 {
		return key, "", false, ""
	}
	if first == last {
		return key[:first], "", false, key[last+1:]
	}
	return key[:first], key[first+1 : last], true, key[last+1:]
}

// shadowInsteadOf applies the `insteadOf` rules of the repository's own config
// to the URL u. The longest match wins (cell 08a of s3p2b-edges-linux). Of two
// rules with one length the first wins: that is not measured.
func shadowInsteadOf(u string, rules [][2]string) (resolved string, changed bool) {
	best := -1
	for i, r := range rules {
		if strings.HasPrefix(u, r[1]) && (best < 0 || len(r[1]) > len(rules[best][1])) {
			best = i
		}
	}
	if best < 0 {
		return u, false
	}
	return rules[best][0] + u[len(rules[best][1]):], true
}

// shadowConfigText is the text of the copy for the settings of a repository.
// settings are those of the config with its included files, each included file
// at the place of its include line (cells 14a to 14k of s3p2b-edges-linux and
// 2a to 2d of s3p2b-edges2-linux). worktree are the settings of the
// `config.worktree` file of the main repository, or nil.
//
// The carry table, with the cell of each row, is in the tests. In short:
//
//   - core.repositoryformatversion: the values 0 and 1 of the config itself.
//     The value 2 gets no block, and the key of an included file never does
//     (cells 15a to 15k of s3p2b-edges-linux).
//   - the keys of shadowBoolKeys, shadowWordKeys and shadowBoolOrWord.
//   - extensions.partialclone with the name of a remote (cell E9).
//   - remote.<name>.url, .promisor and .partialclonefilter, and
//     remote.lfsdefault. The filter ran as `blob:none` only (cell A5).
//   - branch.<name>.remote.
//   - lfs.url, lfs.fetchinclude and lfs.fetchexclude.
//
// A value that does not fit its key is left out with no refusal (cells A3).
// Every key of an extension in an included file is left out: only
// worktreeconfig ran there (cell A7a).
//
// An `insteadOf` rule is applied to each remote URL that it matches, and the
// rule itself is not carried. Each remote that a rule changed gets one more
// block, `[url "<resolved>"]` with the key `insteadOf` and the resolved URL as
// its value. Those blocks stand after the last remote block, in the order of
// their remotes (cells 08a to 08k of s3p2b-edges-linux and 3a to 3e of
// s3p2b-edges2-linux). No cell has a block of another section after the
// remotes, so their place among such blocks is not measured. A rule of an
// included file is not applied: with no remote it is dropped (cell A7a), and
// with a remote it is not measured. A resolved URL gets the tests of
// shadowURL (not measured for a resolved URL that fails them).
//
// Of the `config.worktree` file only core.bare is carried, as one more block
// before the tail (cells 12a to 12f of s3p2b-edges-linux and 9a of
// s3p2b-edges2-linux).
func shadowConfigText(settings, worktree []repoSetting) string {
	var rules [][2]string
	for _, s := range settings {
		section, base, hasSub, name := splitSettingKey(s.key)
		if section == "url" && hasSub && name == "insteadof" && !s.included && s.value != "" {
			rules = append(rules, [2]string{base, s.value})
		}
	}

	var blocks, urlBlocks []string
	afterRemotes := 0
	for _, s := range settings {
		section, sub, hasSub, name := splitSettingKey(s.key)
		value, ok := "", false
		switch {
		case section == "remote" && hasSub:
			if !shadowRemoteName(sub) {
				continue
			}
			switch name {
			case "url":
				if !s.hasValue {
					continue
				}
				resolved, changed := shadowInsteadOf(s.value, rules)
				if !shadowURL(resolved) {
					continue
				}
				value, ok = resolved, true
				if changed {
					urlBlocks = append(urlBlocks, shadowBlock("url", resolved, true, "insteadOf", resolved))
				}
			case "promisor":
				value, ok = shadowBool(s)
			case "partialclonefilter":
				value, ok = s.value, s.value == "blob:none"
			}
			if ok {
				blocks = append(blocks, shadowBlock(section, sub, true, name, value))
				afterRemotes = len(blocks)
			}
			continue
		case section == "branch" && hasSub:
			if name == "remote" && shadowBranchName(sub) && shadowRemoteName(s.value) {
				blocks = append(blocks, shadowBlock(section, sub, true, name, s.value))
			}
			continue
		}

		// A key of another section with a subsection matches no name below.
		switch {
		case s.key == "core.repositoryformatversion":
			value, ok = s.value, !s.included && (s.value == "0" || s.value == "1")
		case section == "extensions" && s.included:
		case shadowBoolKeys[s.key]:
			value, ok = shadowBool(s)
		case shadowWordKeys[s.key] != nil:
			for _, w := range shadowWordKeys[s.key] {
				if s.value == w {
					value, ok = w, true
				}
			}
		case shadowBoolOrWord[s.key] != "":
			if value, ok = s.value, s.value == shadowBoolOrWord[s.key]; !ok {
				value, ok = shadowBool(s)
			}
		case s.key == "extensions.partialclone", s.key == "remote.lfsdefault":
			value, ok = s.value, shadowRemoteName(s.value)
		case s.key == "lfs.url":
			// `https://lfs.example.invalid/o/r` is carried, and the same with a
			// space is not (cells A6 and F6). Another scheme is not measured.
			value, ok = s.value, strings.HasPrefix(s.value, "https://") && shadowURL(s.value)
		case s.key == "lfs.fetchinclude", s.key == "lfs.fetchexclude":
			value, ok = s.value, shadowLFSPattern(s.value)
		}
		if ok {
			blocks = append(blocks, shadowBlock(section, "", false, name, value))
		}
	}

	var b strings.Builder
	for _, block := range blocks[:afterRemotes] {
		b.WriteString(block)
	}
	for _, block := range urlBlocks {
		b.WriteString(block)
	}
	for _, block := range blocks[afterRemotes:] {
		b.WriteString(block)
	}
	for _, s := range worktree {
		if s.key == "core.bare" {
			if v, ok := shadowBool(s); ok {
				b.WriteString(shadowBlock("core", "", false, "bare", v))
			}
		}
	}
	b.WriteString(shadowConfigTail)
	return b.String()
}
