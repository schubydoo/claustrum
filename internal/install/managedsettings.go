package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/schubydoo/claustrum/internal/rpc"
	"github.com/schubydoo/claustrum/internal/sysproc"
)

// The managed settings reader of the managed launcher, the launcher check and the
// child env strip. The overview of the managed launcher is at the top of
// methods_launcher.go of package main.
//
// Every rule below is measured against 89cb6289 unless its comment says "claustrum's
// choice (not measured)". Every text is copied byte for byte from a measured capture.

// Launcher result statuses.
const (
	managedStatusNone   = "none"
	managedStatusUsable = "usable"
	StatusUnusable      = "unusable"
	StatusUnreadable    = "unreadable"
)

// Environment variables of the managed launcher.
const (
	// managedLauncherGateEnv turns the launcher on for -install and -probe-cli. Of
	// the values tried on -install, only "1" does. "true", "1 " (trailing space) and
	// unset do not (measured). managedLauncherGateOn treats every other value as
	// off, which is claustrum's choice. -serve does not read it.
	managedLauncherGateEnv = "CLAUDE_SSH_MANAGED_LAUNCHER"
	// managedSettingsDirEnv moves the managed-settings folder. The reference honors
	// it on Linux and macOS (measured). Windows answers none with it set (measured).
	managedSettingsDirEnv = "CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR"
	// managedWrapperEnv is the env-key form of the value, and also the variable
	// that a launched child loses.
	managedWrapperEnv = "CLAUDE_CODE_PROCESS_WRAPPER"
)

// The managed-settings files, and the size limit of each file (2097152 bytes read,
// 2097153 refused).
const (
	managedSettingsBase     = "managed-settings.json"
	managedSettingsDropIns  = "managed-settings.d"
	managedSettingsMaxBytes = 2 << 20
)

// The reason texts. Each is the exact `reason` value of the reply. On the wire the JSON encoder escapes `&`, `<`, `>`
// and the `""` of the metacharacter text and the empty-word text. The log line writes them raw, as the reference does.
const (
	managedNoLauncher   = "the value is set but contains no launcher — unset it to run without one, or set it to the absolute path of your launcher"
	managedEmptyElement = "the JSON array contains an empty element — remove it, or fill in the value it was a placeholder for"
	managedNotStrings   = "JSON form must be an array of strings"
	managedBadJSONForm  = "value starts with `[` but is not valid JSON"
	managedMetachar     = "the value contains an unquoted shell metacharacter (one of ; | & $ ( ) ` < >) — it is an argv list, not a shell command"
	managedUnterminated = "unterminated double quote"
	managedEmptyToken   = "the value contains an empty `\"\"` token — remove it, or fill in the value it was a placeholder for"
	managedNotAbsolute  = "the launcher must be an absolute path, not a bare name resolved via PATH"

	managedIsDir         = "is a directory"
	managedNotRegular    = "not a regular file"
	managedTooLarge      = "larger than 2 MiB"
	managedInvalidJSON   = "not valid JSON"
	managedNotObject     = "not a JSON object"
	managedDropInsListed = "the drop-in directory could not be listed: "
)

// managedOwnPath, managedScript and managedNotExecutable are three measured texts. Each
// names argv[0] as parsed, in backticks, cut by managedShown.
func managedOwnPath(a0 string) string {
	return "launcher `" + managedShown(a0) + "` is Claude Code's own path"
}

func managedScript(a0 string) string {
	return "launcher `" + managedShown(a0) + "` is a script the SDK would run in place of Claude Code"
}

func managedNotExecutable(a0 string) string {
	return "launcher `" + managedShown(a0) + "` does not exist or is not an executable regular file"
}

// managedShownMax bounds the launcher path inside a reason text. One row is
// measured: a 300-byte path with a 3-byte character at bytes 254-256 kept its first
// 254 bytes, then `…`. The byte limit is 254, 255 or 256, and the row does not tell
// which. claustrum's choice (not measured): 256 bytes, cut back to the start of a
// character, for those three texts alike.
const managedShownMax = 256

func managedShown(s string) string {
	if len(s) <= managedShownMax {
		return s
	}
	n := managedShownMax
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// resolveManagedLauncher reads the managed settings and answers the launcher for a
// CLI at cliPath. It reads the files on every call. Nothing is cached (measured: an
// edit shows on the next call). It never runs the launcher.
//
// The base file is read first, then the drop-ins in name order. A later file with a
// value wins, and source names that file. One unreadable file makes the whole
// answer unreadable. Two of claustrum's choices (not measured) follow. The first
// unreadable file in that order is the one reported. The drop-ins sort by byte order
// (only 10.json before 20.json and s.json before z.json are measured).
func resolveManagedLauncher(cliPath string) rpc.ManagedLauncherResult {
	res, _ := Resolve(cliPath)
	return res
}

// Resolve is resolveManagedLauncher plus the ignored-variable line the resolve
// owes, or "" when it owes none. handleLauncher of package main logs that line
// once per daemon (measured on Linux: two resolves, the line only on the first). More
// than two resolves are not measured. -install and -probe-cli log nothing (not
// measured there).
func Resolve(cliPath string) (rpc.ManagedLauncherResult, string) {
	dir, ignoredVar := managedSettingsDir()
	note := ""
	if ignoredVar {
		note = managedIgnoredVarLine()
	}
	if dir == "" {
		return rpc.ManagedLauncherResult{Status: managedStatusNone}, note
	}
	res := resolveManagedLauncherIn(dir, cliPath)
	return res, note
}

// resolveManagedLauncherIn reads the settings under dir. A later file's refusal
// stands only if no earlier file gave a value. A later value replaces an earlier
// refusal, and a later refusal replaces an earlier one. Measured on Linux against
// b3081b51 with a base file and one drop-in.
func resolveManagedLauncherIn(dir, cliPath string) rpc.ManagedLauncherResult {
	var value, refused, source string
	take := func(path string) *rpc.ManagedLauncherResult {
		v, r, bad := readManagedSettingsValue(path)
		if bad != nil {
			return bad
		}
		switch {
		case v != "":
			value, refused, source = v, "", path
		case r != "" && value == "":
			refused, source = r, path
		}
		return nil
	}
	if bad := take(filepath.Join(dir, managedSettingsBase)); bad != nil {
		return *bad
	}
	dropIns, bad := listManagedDropIns(filepath.Join(dir, managedSettingsDropIns))
	if bad != nil {
		return *bad
	}
	for _, p := range dropIns {
		if bad := take(p); bad != nil {
			return *bad
		}
	}
	if source == "" {
		return rpc.ManagedLauncherResult{Status: managedStatusNone}
	}
	if refused != "" {
		return rpc.ManagedLauncherResult{Status: StatusUnusable, Source: source, Reason: refused}
	}
	argv, reason := parseManagedLauncherValue(value)
	if reason == "" {
		reason = Check(argv, cliPath)
	}
	if reason != "" {
		return rpc.ManagedLauncherResult{Status: StatusUnusable, Source: source, Reason: reason}
	}
	return rpc.ManagedLauncherResult{Status: managedStatusUsable, Argv: argv, Source: source}
}

func managedUnreadable(path, reason string) *rpc.ManagedLauncherResult {
	return &rpc.ManagedLauncherResult{Status: StatusUnreadable, Reason: reason, Path: path}
}

// managedErrText is the errno text of err, for example "permission denied".
func managedErrText(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// readManagedDropInDir is os.ReadDir behind a seam, so a test makes the listing
// fail without a mode-000 folder (root reads one). Production never reassigns it.
var readManagedDropInDir = os.ReadDir

// listManagedDropIns returns the drop-in files of dir in name order. A drop-in name
// ends in ".json" in lower case and does not start with a dot. A folder entry or a
// FIFO entry is skipped, and the FIFO is not opened. A symlink entry is kept, and
// readManagedSettingsValue follows it. A missing dir, or a regular file in its
// place, gives no drop-ins. A dir whose listing fails makes the answer unreadable,
// with the dir as its path.
func listManagedDropIns(dir string) ([]string, *rpc.ManagedLauncherResult) {
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return nil, nil
	}
	ents, err := readManagedDropInDir(dir)
	if err != nil {
		return nil, managedUnreadable(dir, managedDropInsListed+managedErrText(err))
	}
	var out []string
	for _, e := range ents {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, ".") {
			continue
		}
		if t := e.Type(); t&fs.ModeSymlink == 0 && !t.IsRegular() {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	return out, nil
}

// readManagedSettingsFile reads at most one byte past the size limit. It is a seam,
// so a test makes the read fail with "permission denied" without a mode-000 file
// (root reads one). Production never reassigns it.
var readManagedSettingsFile = func(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, managedSettingsMaxBytes+1))
}

// readManagedSettingsValue reads one settings file and returns its launcher value,
// or "" when it has none. The second result is the refusal text of a value that is
// not a string (managedNotOneString), or "". A missing file (also a dangling symlink) has none. The
// checks follow symlinks, so a symlink to a folder is "is a directory".
//
// An empty file, or one of JSON whitespace only, has none. A UTF-8 BOM is dropped.
// A UTF-16LE BOM decodes the rest as UTF-16LE. A UTF-16BE BOM or a raw U+00A0 before
// the JSON is "not valid JSON" (measured). claustrum's choice (not measured): a
// trailing odd byte of UTF-16LE text is dropped.
func readManagedSettingsValue(path string) (string, string, *rpc.ManagedLauncherResult) {
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", "", nil
		}
		return "", "", managedUnreadable(path, managedErrText(err))
	}
	switch {
	case fi.IsDir():
		return "", "", managedUnreadable(path, managedIsDir)
	case !fi.Mode().IsRegular():
		return "", "", managedUnreadable(path, managedNotRegular)
	case fi.Size() > managedSettingsMaxBytes:
		return "", "", managedUnreadable(path, managedTooLarge)
	}
	b, err := readManagedSettingsFile(path)
	if err != nil {
		return "", "", managedUnreadable(path, managedErrText(err))
	}
	if len(b) > managedSettingsMaxBytes {
		return "", "", managedUnreadable(path, managedTooLarge)
	}
	b = decodeManagedSettingsText(b)
	if len(bytes.Trim(b, " \t\r\n")) == 0 {
		return "", "", nil
	}
	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		return "", "", managedUnreadable(path, managedInvalidJSON)
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		return "", "", managedUnreadable(path, managedNotObject)
	}
	v, refused := managedSettingsWrapper(obj)
	return v, refused, nil
}

func decodeManagedSettingsText(b []byte) []byte {
	switch {
	case bytes.HasPrefix(b, []byte{0xef, 0xbb, 0xbf}):
		return b[3:]
	case bytes.HasPrefix(b, []byte{0xff, 0xfe}):
		b = b[2:]
		u := make([]uint16, len(b)/2)
		for i := range u {
			u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
		}
		return []byte(string(utf16.Decode(u)))
	}
	return b
}

// managedSettingsWrapper is the launcher value of one settings file, or the refusal
// text of a value that is not a string. Two keys carry it:
// env.CLAUDE_CODE_PROCESS_WRAPPER and processWrapper. In one file a string env
// value wins, in both key orders. An env value of "" counts as unset.
//
// Measured against b3081b51, with one settings file: a processWrapper of 5 and an
// env value of 5 are refused with managedNotOneString (Linux and macOS), and so are
// a processWrapper of true, [] or {} and an env value of null, true, [] or {}
// (Linux). {"env":"x"} counts as unset (Linux and macOS), and so does a
// processWrapper of null (Linux). A string env value wins over a processWrapper of
// 5, and a string processWrapper wins over an env value of 5 or true (Linux,
// launcher.resolve). be1c1ca5 skipped each of these values (Linux,
// launcher.resolve).
//
// claustrum's choices (not measured): a processWrapper of "" counts as unset, so
// a refused env value answers. When both values are refused, the env refusal
// answers. Each file gives one value, so a later file's processWrapper wins over an
// earlier file's env key.
func managedSettingsWrapper(obj map[string]any) (string, string) {
	envRefused := ""
	if env, ok := obj["env"].(map[string]any); ok {
		if v, set := env[managedWrapperEnv]; set {
			if s, ok := v.(string); ok {
				if s != "" {
					return s, ""
				}
			} else {
				envRefused = managedNotOneString("env."+managedWrapperEnv, v)
			}
		}
	}
	switch v := obj["processWrapper"].(type) {
	case string:
		if v != "" {
			return v, ""
		}
		return "", envRefused
	case nil:
		return "", envRefused
	default:
		if envRefused != "" {
			return "", envRefused
		}
		return "", managedNotOneString("processWrapper", v)
	}
}

// managedNotOneString is the measured refusal text of a launcher value that is not
// a string. key is the settings key as the text names it. v is a decoded JSON value
// that is not a string.
func managedNotOneString(key string, v any) string {
	kind := "an object"
	switch v.(type) {
	case nil:
		kind = "null"
	case float64:
		kind = "a number"
	case bool:
		kind = "a boolean"
	case []any:
		kind = "an array"
	}
	return key + " must be one string (the launcher and its arguments), not " + kind
}

// parseManagedLauncherValue turns a value into an argv, or into a reason text. A
// value that starts with `[` after leading white space is a JSON array. Any other
// value is a plain argv list (splitManagedLauncherWords). Neither form runs a shell.
func parseManagedLauncherValue(v string) ([]string, string) {
	if t := strings.TrimLeftFunc(v, isManagedLauncherSpace); strings.HasPrefix(t, "[") {
		return parseManagedLauncherJSON(t)
	}
	return splitManagedLauncherWords(v)
}

// parseManagedLauncherJSON parses the JSON-array form. JSON escapes decode, and the
// metacharacter check does not apply (measured).
//
// A value that is not one valid JSON document, an element that is not a string, no
// element or an empty first element, and a later empty element each have their own
// measured text. claustrum's choice (not measured): they are checked in that order,
// and a null element is not a string.
func parseManagedLauncherJSON(t string) ([]string, string) {
	var raw []any
	if err := json.Unmarshal([]byte(t), &raw); err != nil {
		return nil, managedBadJSONForm
	}
	argv := make([]string, len(raw))
	for i, e := range raw {
		s, ok := e.(string)
		if !ok {
			return nil, managedNotStrings
		}
		argv[i] = s
	}
	if len(argv) == 0 || argv[0] == "" {
		return nil, managedNoLauncher
	}
	for _, a := range argv[1:] {
		if a == "" {
			return nil, managedEmptyElement
		}
	}
	return argv, ""
}

// managedMetachars are the nine characters that refuse a plain value when they
// stand outside double quotes (each one measured).
const managedMetachars = ";|&$()`<>"

// splitManagedLauncherWords parses the plain form. Measured rules:
//   - White space splits words. The space, U+00A0 and U+FEFF split. U+0085 does not.
//   - Double quotes group, and a quoted metacharacter is fine.
//   - Inside double quotes, \" gives " and \\ gives \. Any other backslash is kept,
//     so "e\nf" keeps its two characters.
//   - Outside double quotes a backslash is a plain character, and a " after it
//     opens a quote.
//   - Single quotes are plain characters.
//   - An unquoted metacharacter and an open quote at the end each have a text.
//   - No word, or an empty first word, has the no-element text. A later empty word ("") has its own text.
//
// claustrum's choices (not measured): the white space is Go's unicode.IsSpace
// without U+0085, plus U+FEFF. That set holds the three measured characters. The
// scan stops at the first metacharacter, before it sees an open quote.
func splitManagedLauncherWords(v string) ([]string, string) {
	var argv []string
	var cur strings.Builder
	inWord, inQuote := false, false
	rs := []rune(v)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if inQuote {
			switch {
			case r == '\\' && i+1 < len(rs) && (rs[i+1] == '"' || rs[i+1] == '\\'):
				i++
				cur.WriteRune(rs[i])
			case r == '"':
				inQuote = false
			default:
				cur.WriteRune(r)
			}
			continue
		}
		switch {
		case isManagedLauncherSpace(r):
			if inWord {
				argv = append(argv, cur.String())
				cur.Reset()
				inWord = false
			}
		case r == '"':
			inQuote, inWord = true, true
		case strings.ContainsRune(managedMetachars, r):
			return nil, managedMetachar
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if inQuote {
		return nil, managedUnterminated
	}
	if inWord {
		argv = append(argv, cur.String())
	}
	if len(argv) == 0 || argv[0] == "" {
		return nil, managedNoLauncher
	}
	for _, a := range argv[1:] {
		if a == "" {
			return nil, managedEmptyToken
		}
	}
	return argv, ""
}

func isManagedLauncherSpace(r rune) bool {
	return (unicode.IsSpace(r) && r != '\u0085') || r == '\ufeff'
}

// managedScriptExts are the argv[0] endings of managedScript. The compare is case-sensitive
// (measured: X.JS is usable).
var managedScriptExts = []string{".js", ".mjs", ".ts", ".tsx", ".jsx"}

// Check checks argv[0] of a launcher and returns a reason text, or ""
// when the launcher is usable. ownPath is the CLI path of launcher.resolve, or the
// command of process.spawn. Measured order: the own-path check is a string compare
// and runs first, before the absolute check and before the existence check. The
// launcher args are not checked. argv[0] is quoted as parsed: it is not cleaned or
// resolved, so a symlink keeps its own path.
//
// claustrum's choice (not measured): the script check runs before the existence
// check, so a missing x.js gets managedScript.
func Check(argv []string, ownPath string) string {
	if len(argv) == 0 {
		return managedNoLauncher
	}
	a0 := argv[0]
	switch {
	case a0 == ownPath:
		return managedOwnPath(a0)
	case !filepath.IsAbs(a0):
		return managedNotAbsolute
	case hasManagedScriptExt(a0):
		return managedScript(a0)
	case !IsExecutableRegular(a0):
		return managedNotExecutable(a0)
	}
	return ""
}

func hasManagedScriptExt(p string) bool {
	for _, ext := range managedScriptExts {
		if strings.HasSuffix(p, ext) {
			return true
		}
	}
	return false
}

// IsExecutableRegular reports whether p (after symlinks) is a regular file that
// this process has permission to execute. exec.LookPath applies that rule for a
// path with a separator.
func IsExecutableRegular(p string) bool {
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	_, err = exec.LookPath(p)
	return err == nil
}

// StripEnv removes the launcher variables from a child env. Every
// spawned child loses CLAUDE_SSH_MANAGED_LAUNCHER and
// CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR. A launched child also loses
// CLAUDE_CODE_PROCESS_WRAPPER. The strip covers the daemon env and the spawn env
// param (measured on Linux and macOS).
//
// claustrum's choice (not measured): the strip runs on Windows too.
func StripEnv(env []string, launched bool) []string {
	env = sysproc.RemoveEnvKey(env, managedLauncherGateEnv)
	env = sysproc.RemoveEnvKey(env, managedSettingsDirEnv)
	if launched {
		env = sysproc.RemoveEnvKey(env, managedWrapperEnv)
	}
	return env
}
