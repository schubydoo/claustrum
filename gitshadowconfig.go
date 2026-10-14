package main

import (
	"os"
	"regexp"
	"strconv"
	"strings"
)

// The text of the settings file that the daemon keeps for one repository. The
// file is a list of blocks. Each carried setting is one block of two lines,
// with a new header before every key and the value in double quotes. The
// blocks are in the order of the repository's file. The file ends with one
// fixed block.
//
// copyKeyRules gives each carried key the rule of its accepted values. A key
// that is not in copyKeyRules, and a value that its rule does not accept, is
// dropped with no reply. testdata/shadow_config_cells.json has the fixture and
// the text of each cell, and TestShadowConfigCells runs them. The rounds are
// s3p2, s3p2b-edges, s3p2b-edges2 and tier0-14 on a Linux VM with git 2.43.0.

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

// shadowValueMaxBytes bounds a carried value. A value of 5000 letters in
// `lfs.fetchexclude` is dropped (cell C1 of round tier0-14, Linux VM). The exact
// bound is not measured.
const shadowValueMaxBytes = 4096

// ruleKind names how a valueRule reads a value.
type ruleKind int

const (
	ruleBool        ruleKind = iota // readBoolWord
	ruleTrueOnly                    // trueOnlyValue
	ruleWords                       // oneOfWords with words
	ruleWordsOrBool                 // wordOrBool with words
	ruleDecimal                     // cappedDecimal with max
	ruleRemoteName                  // remoteNameValue
	ruleText                        // plainTextValue
	ruleURL                         // urlValue
	ruleSharedRepo                  // sharedRepoLine
)

// valueRule is the rule of the accepted values of one key.
type valueRule struct {
	kind  ruleKind
	words []string
	max   int
}

// value is the written form of the value of s, or false for a value that is
// dropped.
func (r valueRule) value(s repoSetting) (string, bool) {
	switch r.kind {
	case ruleBool:
		return readBoolWord(s)
	case ruleTrueOnly:
		return trueOnlyValue(s)
	case ruleWords:
		return oneOfWords(s, r.words...)
	case ruleWordsOrBool:
		return wordOrBool(s, r.words...)
	case ruleDecimal:
		return cappedDecimal(s, r.max)
	case ruleRemoteName:
		return remoteNameValue(s)
	case ruleText:
		return plainTextValue(s)
	case ruleURL:
		return urlValue(s)
	case ruleSharedRepo:
		return sharedRepoLine(s)
	}
	return "", false
}

// copyKeyRules has the 41 carried keys with no subsection, each with the rule
// of its accepted values. The keys of cells C1 to C4 of round tier0-14 are
// measured (Linux VM). The other keys and the values beyond those cells are not
// measured. `extensions.worktreeconfig` is not in it, so it is never written.
// Keys with a subsection have their own rules in copyBlockOf.
var copyKeyRules = map[string]valueRule{
	"checkout.thresholdforparallelism": {kind: ruleDecimal, max: 1 << 30},
	"checkout.workers":                 {kind: ruleDecimal, max: 64},
	"core.autocrlf":                    {kind: ruleWordsOrBool, words: []string{"input"}},
	"core.bare":                        {kind: ruleBool},
	"core.checkstat":                   {kind: ruleWords, words: []string{"default", "minimal"}},
	"core.eol":                         {kind: ruleWords, words: []string{"lf", "crlf", "native"}},
	"core.filemode":                    {kind: ruleBool},
	"core.ignorecase":                  {kind: ruleBool},
	"core.logallrefupdates":            {kind: ruleWordsOrBool, words: []string{"always"}},
	"core.longpaths":                   {kind: ruleBool},
	"core.precomposeunicode":           {kind: ruleBool},
	"core.preloadindex":                {kind: ruleBool},
	"core.protecthfs":                  {kind: ruleTrueOnly},
	"core.protectntfs":                 {kind: ruleTrueOnly},
	"core.quotepath":                   {kind: ruleBool},
	"core.repositoryformatversion":     {kind: ruleWords, words: []string{"0", "1"}},
	"core.safecrlf":                    {kind: ruleWordsOrBool, words: []string{"warn"}},
	"core.sharedrepository":            {kind: ruleSharedRepo},
	"core.sparsecheckout":              {kind: ruleBool},
	"core.sparsecheckoutcone":          {kind: ruleBool},
	"core.splitindex":                  {kind: ruleBool},
	"core.symlinks":                    {kind: ruleBool},
	"core.trustctime":                  {kind: ruleBool},
	"core.untrackedcache":              {kind: ruleWordsOrBool, words: []string{"keep"}},
	"extensions.compatobjectformat":    {kind: ruleWords, words: []string{"sha1", "sha256"}},
	"extensions.noop":                  {kind: ruleBool},
	"extensions.noop-v1":               {kind: ruleBool},
	"extensions.objectformat":          {kind: ruleWords, words: []string{"sha1", "sha256"}},
	"extensions.partialclone":          {kind: ruleRemoteName},
	"extensions.preciousobjects":       {kind: ruleBool},
	"extensions.refstorage":            {kind: ruleWords, words: []string{"files", "reftable"}},
	"extensions.relativeworktrees":     {kind: ruleBool},
	"extensions.submodulepathconfig":   {kind: ruleBool},
	"feature.manyfiles":                {kind: ruleBool},
	"index.skiphash":                   {kind: ruleBool},
	"index.sparse":                     {kind: ruleBool},
	"index.version":                    {kind: ruleWords, words: []string{"2", "3", "4"}},
	"lfs.fetchexclude":                 {kind: ruleText},
	"lfs.fetchinclude":                 {kind: ruleText},
	"lfs.url":                          {kind: ruleURL},
	"remote.lfsdefault":                {kind: ruleRemoteName},
}

// shadowValue is s in the written form of its key, or false for a key that is
// not in copyKeyRules and for a value that the copy drops.
func shadowValue(s repoSetting) (string, bool) {
	rule, ok := copyKeyRules[s.key]
	if !ok || len(s.value) > shadowValueMaxBytes {
		return "", false
	}
	return rule.value(s)
}

// readBoolWord is the bool rule. No value gives true. Empty, `no`, `off` and
// `false` in any letter case give false, and `yes`, `on` and `true` give true.
// One to nine decimal digits give true unless each digit is 0. Every other
// value fails. Cells 03a to 03m of s3p2b-edges-linux and C1 and C3a of tier0-14
// measured the words, and C1 measured `1`. `worktreeConfig = 2` and `10` are on
// (cell C3c of tier0-14 and cell K1 of tier0-16). For `core.sharedrepository`,
// no value, `on` and `9` give `group` (cells Q1 to Q3 of tier0-18). All on a
// Linux VM. The digit bound and `0` are not measured.
func readBoolWord(s repoSetting) (string, bool) {
	if !s.hasValue {
		return "true", true
	}
	switch strings.ToLower(s.value) {
	case "true", "yes", "on":
		return "true", true
	case "false", "no", "off", "":
		return "false", true
	}
	if !decimalDigits(s.value) {
		return "", false
	}
	if strings.Trim(s.value, "0") != "" {
		return "true", true
	}
	return "false", true
}

// decimalDigits reports whether v is one to nine decimal digits.
func decimalDigits(v string) bool {
	return len(v) >= 1 && len(v) <= 9 && strings.Trim(v, "0123456789") == ""
}

// trueOnlyValue keeps a value that the bool rule reads as true (cell C1 of
// tier0-14).
func trueOnlyValue(s repoSetting) (string, bool) {
	if v, ok := readBoolWord(s); ok && v == "true" {
		return v, true
	}
	return "", false
}

// oneOfWords keeps a value whose lower case is one of words, written in lower
// case. A key with no value fails.
func oneOfWords(s repoSetting, words ...string) (string, bool) {
	if !s.hasValue {
		return "", false
	}
	v := strings.ToLower(s.value)
	for _, w := range words {
		if v == w {
			return w, true
		}
	}
	return "", false
}

// wordOrBool tries words first and then the bool rule.
func wordOrBool(s repoSetting, words ...string) (string, bool) {
	if v, ok := oneOfWords(s, words...); ok {
		return v, true
	}
	return readBoolWord(s)
}

// cappedDecimal keeps one to nine decimal digits, read as a decimal number and
// cut down to max (cell C1 of tier0-14: 100 gives 64, 007 gives 7).
func cappedDecimal(s repoSetting, max int) (string, bool) {
	if !s.hasValue || !decimalDigits(s.value) {
		return "", false
	}
	n, _ := strconv.Atoi(s.value)
	return strconv.Itoa(min(n, max)), true
}

// shadowNamePattern holds a remote name and a value that names a remote. Cells
// C1 and C5 of tier0-14 keep `1up` and `a..b` and drop `a/-b`. The rest of the
// pattern is not measured.
var shadowNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+@-]*(?:/[A-Za-z0-9_+@][A-Za-z0-9._+@-]*)*$`)

func remoteNameValue(s repoSetting) (string, bool) {
	return s.value, s.hasValue && shadowNamePattern.MatchString(s.value)
}

// hasControlByte reports whether v holds a byte from 0x00 to 0x1f or 0x7f.
func hasControlByte(v string) bool {
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] == 0x7f {
			return true
		}
	}
	return false
}

// plainTextValue keeps a value with no control byte. An empty value passes
// (cell C1 of tier0-14).
func plainTextValue(s repoSetting) (string, bool) {
	return s.value, s.hasValue && !hasControlByte(s.value)
}

// shadowFilterPattern holds the accepted values of
// `remote.<name>.partialclonefilter`. `tree:0` and an empty value pass (cell
// C1 of tier0-14).
var shadowFilterPattern = regexp.MustCompile(`^[A-Za-z0-9:=,+.]*$`)

func filterValue(s repoSetting) (string, bool) {
	return s.value, s.hasValue && shadowFilterPattern.MatchString(s.value)
}

// urlValue keeps a value that shadowURL accepts.
func urlValue(s repoSetting) (string, bool) {
	return s.value, s.hasValue && shadowURL(s.value)
}

// lfsAccessWords are the values of `lfs.<url>.access` (cell C1 of tier0-14).
var lfsAccessWords = []string{"basic", "private", "negotiate", "none"}

// sharedRepoLine is the copy line of `core.sharedrepository`.
//
// `umask`, `group` and `everybody` stay as written, and `all` and `world` give
// `everybody`. A value that is not one to nine octal digits takes the bool
// rule: true gives `group`, false gives `umask`. The octal numbers 0, 1 and 2
// give `umask`, `group` and `everybody`. A larger number is masked with 0666.
// It is kept as `0` and three octal digits only when the owner can read and
// write. Cell C2 of tier0-14 measured no value, empty, `yes`, `1`, `TRUE`, `0`,
// `2`, `660` and `0777` (Linux VM). The 18 rows of group P5 of
// val-s3p2b2-6-linux measured `group`, `true`, `all`, `world`, `everybody`,
// `0660`, `0640` and `umask`. Other forms are not measured.
//
// The mode of the folder `worktrees` follows the last such line of the copy
// (sharedRepositoryOf).
func sharedRepoLine(s repoSetting) (string, bool) {
	if s.hasValue {
		switch s.value {
		case "umask", "group", "everybody":
			return s.value, true
		case "all", "world":
			return "everybody", true
		}
		if n, err := strconv.ParseUint(s.value, 8, 32); err == nil && decimalDigits(s.value) {
			switch n {
			case 0:
				return "umask", true
			case 1:
				return "group", true
			case 2:
				return "everybody", true
			}
			m := n & 0o666
			if m&0o600 != 0o600 {
				return "", false
			}
			return "0" + strconv.FormatUint(m|0o1000, 8)[1:], true
		}
	}
	switch v, ok := readBoolWord(s); {
	case !ok:
		return "", false
	case v == "true":
		return "group", true
	}
	return "umask", true
}

// sharedRepository is how the daemon reads one `core.sharedRepository` value
// for the mode of the folder `worktrees`. The zero value is a value that gives
// no mode call.
type sharedRepository struct {
	// sets is true when the folder `worktrees` that the daemon makes gets a mode
	// call. bits are the mode bits of that call. With adds they are added to the
	// mode of the mkdir, and without it they are the whole mode.
	sets bool
	adds bool
	bits os.FileMode
}

// sharedRepositoryOf reads line, the last `core.sharedrepository` line of the
// copy (sharedRepoLine), for the mode call of the folder `worktrees`.
//
// `group` and `everybody` start the mode call, and `umask` starts none (the 18
// rows of group P5 of val-s3p2b2-6-linux, Linux VM). A line of the form `0xxx`
// starts it with those bits. With umask 0002 the folder gets 2775 for `group`
// from `yes` (cell H1 of tier0-15), 2770 for `0660` from `660` (cell S1 of
// tier0-16) and 2775 for `everybody` from `2` (cell S2 of tier0-16). With nine
// values whose last is `0777` the line is `0666`, and the folder gets 2777 (cell
// C2 of tier0-14 and tier0-15). All on a Linux VM.
func sharedRepositoryOf(line string) sharedRepository {
	switch line {
	case "group":
		return sharedRepository{sets: true, adds: true, bits: 0o660}
	case "everybody":
		return sharedRepository{sets: true, adds: true, bits: 0o664}
	}
	if len(line) != 4 || line[0] != '0' {
		return sharedRepository{}
	}
	n, err := strconv.ParseUint(line, 8, 9)
	if err != nil {
		return sharedRepository{}
	}
	return sharedRepository{sets: true, bits: os.FileMode(n)}
}

// folderMode is the mode that the folder `worktrees` gets after its mkdir gave
// it the mode made. ok is false for no mode call.
//
// The rule is fitted to the measured rows. The bits of the value are added to
// the mode of the mkdir, or they replace it. Then each read bit gives its
// execute bit, and the set-group-ID bit is set. On 5fd08069 the folder is
// `d 2775` for `group`, `true`, `all`, `world` and `everybody`, `d 2770` for
// `0660` and `d 2750` for `0640`, each with a daemon umask of 022 and of 002.
// A trace has `fchmod(11, 02770)` by the daemon for `0660` (the 18 rows of
// group P5 of val-s3p2b2-6-linux, Linux VM). It is the rule that git has for a
// folder of such a repository. `0666` gives 2777 (cell C2 of tier0-14, Linux
// VM). Another octal value is not measured.
func (r sharedRepository) folderMode(made os.FileMode) (mode os.FileMode, ok bool) {
	if !r.sets {
		return 0, false
	}
	mode = r.bits
	if r.adds {
		mode |= made.Perm()
	}
	mode |= (mode & 0o444) >> 2
	return mode | os.ModeSetgid, true
}

func asciiLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func asciiAlnum(c byte) bool {
	return asciiLetter(c) || (c >= '0' && c <= '9')
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
// then one colon, then a path. The path can be empty: `example.invalid:` is
// kept (cell C5 of tier0-14, Linux VM). A host of one letter is the Windows
// drive form. It is left out on every system: Linux and macOS did not run it.
// A first `-` after the scheme is not measured and is left out.
//
// An `ssh://` URL with a user and a password before the host is left out, as
// `ssh://user:secret@example.invalid/x` (cell C5 of tier0-14). An `https://`
// URL with them is carried (cell A5 of s3p2-linux). `http://` and `git://` with
// them are carried too (not measured).
func shadowURL(u string) bool {
	if u == "" || !shadowURLBytes(u) {
		return false
	}
	for _, scheme := range []string{"https://", "http://", "ssh://", "git://"} {
		if rest, ok := strings.CutPrefix(u, scheme); ok {
			host, _, _ := strings.Cut(rest, "/")
			user, _, hasUser := strings.Cut(host, "@")
			password := scheme == "ssh://" && hasUser && strings.Contains(user, ":")
			return rest != "" && rest[0] != '-' && !password
		}
	}
	host, path, ok := strings.Cut(u, ":")
	if !ok || len(host) < 2 || strings.HasPrefix(path, ":") || strings.Contains(u, "://") {
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

// shadowInsteadOf applies the `insteadOf` rules of the config and its plain
// includes to the URL u. The longest match wins (cell 08a of s3p2b-edges-linux). Of two
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

// droppedFromInclude reports whether a setting of an included file is dropped:
// the format version, each extension, core.bare and each filter key (cell C4 of
// tier0-14 for core.bare, cells 15a to 15k of s3p2b-edges-linux for the version
// and cell A7a for an extension). A filter key there is not measured.
func droppedFromInclude(key string) bool {
	return key == "core.repositoryformatversion" || key == "core.bare" ||
		strings.HasPrefix(key, "extensions.") || strings.HasPrefix(key, "filter.")
}

// copyBlockOf is the block of one setting, or "" for a setting that the copy
// drops. A URL that an `insteadOf` rule rewrote also gives its url block.
func copyBlockOf(s repoSetting, rules [][2]string) (block, urlBlock string) {
	if s.included && droppedFromInclude(s.key) {
		return "", ""
	}
	section, sub, hasSub, name := splitSettingKey(s.key)
	if len(s.value) > shadowValueMaxBytes {
		return "", ""
	}
	if s.key == "lfs.url" {
		return shadowURLEntry(s, rules, section, "", false, name)
	}
	if !hasSub {
		value, ok := shadowValue(s)
		if !ok {
			return "", ""
		}
		return shadowBlock(section, "", false, name, value), ""
	}
	// The forms with a subsection. Cells C1 and C5 of tier0-14 measured the
	// forms that they show, and the rest is not measured.
	value, ok := "", false
	switch {
	case section == "remote" && shadowNamePattern.MatchString(sub):
		switch name {
		case "url", "lfsurl":
			return shadowURLEntry(s, rules, section, sub, true, name)
		case "promisor":
			value, ok = readBoolWord(s)
		case "partialclonefilter":
			value, ok = filterValue(s)
		}
	case section == "lfs" && name == "access" && shadowURL(sub):
		value, ok = oneOfWords(s, lfsAccessWords...)
	case section == "branch" && name == "remote" && !hasControlByte(sub) && !strings.HasPrefix(sub, "-"):
		value, ok = remoteNameValue(s)
	}
	if !ok {
		return "", ""
	}
	return shadowBlock(section, sub, true, name, value), ""
}

// shadowURLEntry is the block of a URL setting after the `insteadOf` step, and
// the url block of a rewritten URL (cells C3a and C4 of tier0-14). The length
// bound tests the value before the rewrite. The length after it is not measured.
func shadowURLEntry(s repoSetting, rules [][2]string, section, sub string, hasSub bool, name string) (block, urlBlock string) {
	if !s.hasValue {
		return "", ""
	}
	resolved, changed := shadowInsteadOf(s.value, rules)
	if !shadowURL(resolved) {
		return "", ""
	}
	if changed {
		urlBlock = shadowBlock("url", resolved, true, "insteadOf", resolved)
	}
	return shadowBlock(section, sub, hasSub, name, resolved), urlBlock
}

// shadowConfigText is the text of the copy for the settings of a repository.
// settings are those of the config with its included files, each included file
// at the place of its include line (cells 14a to 14k of s3p2b-edges-linux and
// 2a to 2d of s3p2b-edges2-linux). worktree are the settings of the
// `config.worktree` file of the main repository, or nil.
//
// The copy has each kept entry in the order of the entries, each as its own
// block. Then comes the last core.bare of the `config.worktree` file, as one
// block. Then come the url blocks of the rewritten URLs, and then the tail
// (cells C1 to C5 of tier0-14, Linux VM).
//
// An `insteadOf` rule is applied to each URL that it matches, and the rule
// itself is not carried. A rule of a plainly included file applies too: it
// rewrites `lfs.url` in cell C4 of tier0-14 (Linux VM). A rule of a conditional
// include is not read (not measured).
func shadowConfigText(settings, worktree []repoSetting) string {
	var rules [][2]string
	for _, s := range settings {
		section, base, hasSub, name := splitSettingKey(s.key)
		if section == "url" && hasSub && name == "insteadof" && s.value != "" {
			rules = append(rules, [2]string{base, s.value})
		}
	}

	var b strings.Builder
	var urlBlocks []string
	for _, s := range settings {
		block, urlBlock := copyBlockOf(s, rules)
		b.WriteString(block)
		if urlBlock != "" {
			urlBlocks = append(urlBlocks, urlBlock)
		}
	}
	// A last core.bare whose value fails the bool rule gives no block (not
	// measured).
	var bare *repoSetting
	for i := range worktree {
		if worktree[i].key == "core.bare" {
			bare = &worktree[i]
		}
	}
	if bare != nil {
		if v, ok := readBoolWord(*bare); ok {
			b.WriteString(shadowBlock("core", "", false, "bare", v))
		}
	}
	for _, block := range urlBlocks {
		b.WriteString(block)
	}
	b.WriteString(shadowConfigTail)
	return b.String()
}
