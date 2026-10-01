package main

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
	"sync"
	"syscall"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// The managed launcher (reference build 89cb6289). A host administrator names a
// launcher in the managed settings. The launcher is a program that runs the Claude
// Code CLI on this host: the daemon puts its argv in front of the command.
// Three surfaces use it:
//
//   - launcher.resolve reads the managed settings and answers none, usable,
//     unusable or unreadable (handleLauncher below).
//   - process.spawn takes a `launcher` param and runs the child through it, or
//     refuses the spawn (refuseManagedLauncher, spawnVia in process.go).
//   - -install and -probe-cli run the CLI through the launcher, but only when
//     CLAUDE_SSH_MANAGED_LAUNCHER=1 (managedlauncher_run.go).
//
// Every rule below is VM-measured against 89cb6289 unless its comment says
// "claustrum's choice (not measured)". Every text is copied byte for byte from a VM
// capture.
//
// The word "launcher" alone already names the -serve parent process in server.go
// and dahandoff.go, so the identifiers here say "managed launcher".

// Launcher result statuses.
const (
	managedStatusNone       = "none"
	managedStatusUsable     = "usable"
	managedStatusUnusable   = "unusable"
	managedStatusUnreadable = "unreadable"
)

// Environment variables of the managed launcher.
const (
	// managedLauncherGateEnv turns the launcher on for -install and -probe-cli. Of
	// the values tried on -install, only "1" does. "true", "1 " (trailing space) and
	// unset do not (measured). managedLauncherGateOn treats every other value as
	// off, which is claustrum's choice. -serve does not read it.
	managedLauncherGateEnv = "CLAUDE_SSH_MANAGED_LAUNCHER"
	// managedSettingsDirEnv moves the managed-settings folder. The reference honors
	// it on Linux (measured). claustrum honors it on macOS too: that is claustrum's
	// choice (not measured on macOS). Windows answers none with it set (measured).
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

// The reason texts. Each is the exact `reason` value of the reply. On the wire the
// JSON encoder escapes `&`, `<`, `>` and the `""` of T5 and T7. The log line writes
// them raw, as the reference does.
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

// managedOwnPath, managedScript and managedNotExecutable are T9, T10 and T11. Each
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
// 254 bytes, then `…`. The byte limit is 254, 255 or 256, and the row does not
// tell which. claustrum's choice (not measured): 256 bytes, cut back to the start
// of a character, for T9, T10 and T11 alike.
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
func resolveManagedLauncher(cliPath string) managedLauncherResult {
	res, _ := resolveManagedLauncherNoted(cliPath)
	return res
}

// resolveManagedLauncherNoted is resolveManagedLauncher plus the ignored-variable
// line the resolve owes, or "" when it owes none. handleLauncher logs that line
// once per daemon (row RXs, Linux: two resolves, the line only on the first). More
// than two resolves are not measured. -install and -probe-cli log nothing (not
// measured there).
func resolveManagedLauncherNoted(cliPath string) (managedLauncherResult, string) {
	dir, ignoredVar := managedSettingsDir()
	note := ""
	if ignoredVar {
		note = managedIgnoredVarLine()
	}
	if dir == "" {
		return managedLauncherResult{Status: managedStatusNone}, note
	}
	res := resolveManagedLauncherIn(dir, cliPath)
	return res, note
}

// resolveManagedLauncherIn reads the settings under dir.
func resolveManagedLauncherIn(dir, cliPath string) managedLauncherResult {
	var value, source string
	take := func(path string) *managedLauncherResult {
		v, bad := readManagedSettingsValue(path)
		if bad != nil {
			return bad
		}
		if v != "" {
			value, source = v, path
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
		return managedLauncherResult{Status: managedStatusNone}
	}
	argv, reason := parseManagedLauncherValue(value)
	if reason == "" {
		reason = checkManagedLauncher(argv, cliPath)
	}
	if reason != "" {
		return managedLauncherResult{Status: managedStatusUnusable, Source: source, Reason: reason}
	}
	return managedLauncherResult{Status: managedStatusUsable, Argv: argv, Source: source}
}

func managedUnreadable(path, reason string) *managedLauncherResult {
	return &managedLauncherResult{Status: managedStatusUnreadable, Reason: reason, Path: path}
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
func listManagedDropIns(dir string) ([]string, *managedLauncherResult) {
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
// or "" when it has none. A missing file (also a dangling symlink) has none. The
// checks follow symlinks, so a symlink to a folder is "is a directory".
//
// An empty file, or one of JSON whitespace only, has none. A UTF-8 BOM is dropped.
// A UTF-16LE BOM decodes the rest as UTF-16LE. A UTF-16BE BOM or a raw U+00A0 before
// the JSON is "not valid JSON" (measured). claustrum's choice (not measured): a
// trailing odd byte of UTF-16LE text is dropped.
func readManagedSettingsValue(path string) (string, *managedLauncherResult) {
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", managedUnreadable(path, managedErrText(err))
	}
	switch {
	case fi.IsDir():
		return "", managedUnreadable(path, managedIsDir)
	case !fi.Mode().IsRegular():
		return "", managedUnreadable(path, managedNotRegular)
	case fi.Size() > managedSettingsMaxBytes:
		return "", managedUnreadable(path, managedTooLarge)
	}
	b, err := readManagedSettingsFile(path)
	if err != nil {
		return "", managedUnreadable(path, managedErrText(err))
	}
	if len(b) > managedSettingsMaxBytes {
		return "", managedUnreadable(path, managedTooLarge)
	}
	b = decodeManagedSettingsText(b)
	if len(bytes.Trim(b, " \t\r\n")) == 0 {
		return "", nil
	}
	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		return "", managedUnreadable(path, managedInvalidJSON)
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		return "", managedUnreadable(path, managedNotObject)
	}
	return managedSettingsWrapper(obj), nil
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

// managedSettingsWrapper is the launcher value of one settings file. Two keys carry
// it: env.CLAUDE_CODE_PROCESS_WRAPPER and processWrapper. In one file the env key
// wins, in both key orders. An env value of "" counts as unset. A value of the wrong
// type is skipped (measured: a later file's wrong-type value leaves the earlier
// value in place).
//
// Two of claustrum's choices (not measured) follow. A processWrapper of "" counts
// as unset too. Each file gives one value, so a later file's processWrapper wins
// over an earlier file's env key.
func managedSettingsWrapper(obj map[string]any) string {
	if env, ok := obj["env"].(map[string]any); ok {
		if s, ok := env[managedWrapperEnv].(string); ok && s != "" {
			return s
		}
	}
	s, _ := obj["processWrapper"].(string)
	return s
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
// A value that is not one valid JSON document is T4. An element that is not a
// string is T3. No element, or an empty first element, is T1. A later empty
// element is T2. claustrum's choice (not measured): T4, T3, T1 and T2 are checked in
// that order, and a null element is not a string.
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
// stand outside double quotes (T5, each one measured).
const managedMetachars = ";|&$()`<>"

// splitManagedLauncherWords parses the plain form. Measured rules:
//   - White space splits words. The space, U+00A0 and U+FEFF split. U+0085 does not.
//   - Double quotes group, and a quoted metacharacter is fine.
//   - Inside double quotes, \" gives " and \\ gives \. Any other backslash is kept,
//     so "e\nf" keeps its two characters.
//   - Outside double quotes a backslash is a plain character, and a " after it
//     opens a quote.
//   - Single quotes are plain characters.
//   - An unquoted metacharacter is T5. An open quote at the end is T6.
//   - No word, or an empty first word, is T1. A later empty word ("") is T7.
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

// managedScriptExts are the argv[0] endings of T10. The compare is case-sensitive
// (measured: X.JS is usable).
var managedScriptExts = []string{".js", ".mjs", ".ts", ".tsx", ".jsx"}

// checkManagedLauncher checks argv[0] of a launcher and returns a reason text, or ""
// when the launcher is usable. ownPath is the CLI path of launcher.resolve, or the
// command of process.spawn. Measured order: the own-path check is a string compare
// and runs first, before the absolute check and before the existence check. The
// launcher args are not checked. argv[0] is quoted as parsed: it is not cleaned or
// resolved, so a symlink keeps its own path.
//
// claustrum's choice (not measured): the script check runs before the existence
// check, so a missing x.js is T10.
func checkManagedLauncher(argv []string, ownPath string) string {
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
	case !isExecutableRegular(a0):
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

// isExecutableRegular reports whether p (after symlinks) is a regular file that
// this process has permission to execute. exec.LookPath applies that rule for a
// path with a separator.
func isExecutableRegular(p string) bool {
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	_, err = exec.LookPath(p)
	return err == nil
}

// managedLauncherLog writes the [LauncherHandler] line of a launcher.resolve
// answer. An unusable or unreadable answer writes one line, a usable or none answer
// writes none. A repeat of the last answer writes no line, and a usable answer in
// between makes the next equal refusal log again (measured).
//
// claustrum's choices (not measured) follow. The key is the whole line, so the same
// reason from another file logs again. A none answer resets it like a usable one.
// The key lives for one daemon. The lines log at WARN.
type managedLauncherLog struct {
	mu   sync.Mutex
	last string
	// ignoredVarLogged is set once the ignored-variable line was written.
	ignoredVarLogged bool
}

// firstIgnoredVar reports true the first time only, so the ignored-variable line
// logs once per daemon.
func (l *managedLauncherLog) firstIgnoredVar() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	first := !l.ignoredVarLogged
	l.ignoredVarLogged = true
	return first
}

func (l *managedLauncherLog) note(res managedLauncherResult) {
	line := ""
	switch res.Status {
	case managedStatusUnusable:
		line = "[LauncherHandler] managed launcher from " + res.Source + " refused: " + res.Reason
	case managedStatusUnreadable:
		line = "[LauncherHandler] managed settings unreadable: " + res.Path + ": " + res.Reason
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if line == l.last {
		return
	}
	l.last = line
	if line != "" {
		logWarnf("%s", line)
	}
}

type managedLauncherParams struct {
	CliPath string `json:"cliPath"`
}

// expandPaths is a no-op. claustrum's choice (not measured): cliPath is not
// `~`-expanded, so the own-path check compares the caller's text.
func (p *managedLauncherParams) expandPaths() {}

// handleLauncher serves the launcher namespace. Its one method is launcher.resolve.
// No params member, a non-object params or a non-string cliPath is -32602 "Invalid
// params". {}, null params, a null or empty cliPath and a wrong key are -32602
// "cliPath is required". Any other string is a cliPath, a relative one too.
func (s *server) handleLauncher(req *request) response {
	if req.Method != "launcher.resolve" {
		return unknownMethod(req)
	}
	if bad := needParams(req); bad != nil {
		return *bad
	}
	var p managedLauncherParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	if p.CliPath == "" {
		return errResult(req.ID, codeInvalidParam, "cliPath is required")
	}
	res, note := resolveManagedLauncherNoted(p.CliPath)
	if note != "" && s.managedLog.firstIgnoredVar() {
		logInfof("%s", note)
	}
	s.managedLog.note(res)
	return okResult(req.ID, res)
}

// The process.spawn texts of the launcher param.
const (
	managedSpawnRelative  = "command must be an absolute path when a launcher is given"
	managedSpawnCannotUse = "the managed launcher cannot be used: "
	managedNoInterpreter  = "its interpreter was not found (the program on its #! line, or the loader of an ELF binary; a script saved with Windows CRLF line endings fails this way)"
)

// spawnRefusal is a spawn failure that carries its own JSON-RPC code.
type spawnRefusal struct {
	code int
	msg  string
}

func (e *spawnRefusal) Error() string { return e.msg }

// refuseManagedLauncher runs the process.spawn checks of a launcher, before the cwd
// check and before the env is built. The measured order: on Windows any launcher is
// refused, before the cwd check. A relative command is -32602, before any launcher
// check. Then the launcher checks give -32004. An empty launcher is T1.
//
// claustrum's choice (not measured): the Windows refusal comes before the relative
// command check.
func refuseManagedLauncher(launcher []string, command string) error {
	if r := managedLauncherHostRefusal(); r != "" {
		return &spawnRefusal{code: codeLauncherUnusable, msg: managedSpawnCannotUse + r}
	}
	if !filepath.IsAbs(command) {
		return &spawnRefusal{code: codeInvalidParam, msg: managedSpawnRelative}
	}
	if r := checkManagedLauncher(launcher, command); r != "" {
		return &spawnRefusal{code: codeLauncherUnusable, msg: managedSpawnCannotUse + r}
	}
	return nil
}

// launchedCommandError checks the command of a launched spawn before the launcher
// starts. A command that is missing, a folder or not executable gives the same
// -32603 "fork/exec <command>: <reason>" frame as a spawn without a launcher. The
// launcher does not run (measured for a missing file, a folder and a 0644 file).
//
// claustrum's choice (not measured): this check runs before the launcher starts, so
// a bad command wins over a launcher that does not start.
func launchedCommandError(command string) error {
	if _, err := os.Stat(command); err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			return &fs.PathError{Op: "fork/exec", Path: command, Err: pe.Err}
		}
		return err
	}
	if !isExecutableRegular(command) {
		return &fs.PathError{Op: "fork/exec", Path: command, Err: syscall.EACCES}
	}
	return nil
}

// managedLauncherStartError turns the start failure of a launcher into the -32005
// refusal. err comes from exec.Cmd.Start, or it is the "fork/exec <path>: <reason>"
// text of the exec-child trampoline. A missing file at exec (the #! interpreter or
// the ELF loader) is the interpreter text. Any other reason is its errno text, as
// "exec format error" is (measured).
//
// claustrum's choice (not measured): the reasons other than those two are the
// errno text as it stands.
func managedLauncherStartError(a0 string, err error) error {
	reason := err.Error()
	var pe *fs.PathError
	if errors.As(err, &pe) {
		reason = pe.Err.Error()
	} else {
		reason = strings.TrimPrefix(reason, "fork/exec "+a0+": ")
	}
	if reason == syscall.ENOENT.Error() {
		reason = managedNoInterpreter
	}
	return &spawnRefusal{code: codeLauncherStart, msg: "the managed launcher " + a0 + " could not be started: " + reason}
}

// stripManagedLauncherEnv removes the launcher variables from a child env. Every
// spawned child loses CLAUDE_SSH_MANAGED_LAUNCHER and
// CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR. A launched child also loses
// CLAUDE_CODE_PROCESS_WRAPPER. The strip covers the daemon env and the spawn env
// param (measured on Linux and macOS).
//
// claustrum's choice (not measured): the strip runs on Windows too.
func stripManagedLauncherEnv(env []string, launched bool) []string {
	env = removeEnvKey(env, managedLauncherGateEnv)
	env = removeEnvKey(env, managedSettingsDirEnv)
	if launched {
		env = removeEnvKey(env, managedWrapperEnv)
	}
	return env
}
