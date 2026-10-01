//go:build !windows

package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf16"
)

// launcher.resolve and process.spawn rows of the 89cb6289 VM captures, on unix. The
// settings folder is a temp folder named by CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR, so
// no test reads or writes a system folder. A launcher that runs is a symlink to
// this test binary in a helper mode (helperproc_test.go), so no test depends on a
// /bin program. An unreadable file or folder comes from a seam, not from a mode-000
// fixture, which root reads anyway.

// fifoWatchdog guards a resolve over a FIFO fixture. A guard regression opens the
// FIFO and blocks the test for its whole deadline instead of failing. After 5 s the
// watchdog opens the FIFO for writing without blocking: with no reader that fails
// with ENXIO, and with a blocked reader it succeeds and releases it. The returned
// check fails the test when the open succeeded.
func fifoWatchdog(t *testing.T, fifo string) (check func()) {
	t.Helper()
	var opened atomic.Bool
	done := make(chan struct{})
	timer := time.AfterFunc(5*time.Second, func() {
		defer close(done)
		fd, err := syscall.Open(fifo, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			opened.Store(true)
			_ = syscall.Close(fd)
		}
	})
	return func() {
		t.Helper()
		// Stop reports false once the function fired or is firing. Join it then. The
		// blocked reader wakes when the watchdog's Open returns, which is before the
		// Store, so the read below can otherwise come first.
		if !timer.Stop() {
			<-done
		}
		if opened.Load() {
			t.Errorf("the resolve opened the FIFO %s and blocked on it", fifo)
		}
	}
}

// tempManagedSettingsDir points the managed-settings folder at a fresh temp folder and returns it.
// It also points the system folder at an absent path. managedSettingsDir reads the
// system folder first, and a policy on the host there makes the resolve ignore the
// variable.
func tempManagedSettingsDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	setManagedSystemDir(t, filepath.Join(t.TempDir(), "absent-system"))
	t.Setenv(managedSettingsDirEnv, d)
	return d
}

// writeFileMode writes b to p with exactly mode, creating the parent folders.
func writeFileMode(t *testing.T, p string, b []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil { // WriteFile's mode passes through the umask
		t.Fatal(err)
	}
}

// mkExec makes p an executable regular file. Resolve only checks it, never runs it.
func mkExec(t *testing.T, p string) string {
	t.Helper()
	writeFileMode(t, p, []byte("#\n"), 0o755)
	return p
}

// jsonText is v as a JSON string literal, for building a settings file.
func jsonText(t *testing.T, v string) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func envSettings(t *testing.T, v string) string {
	return `{"env":{"CLAUDE_CODE_PROCESS_WRAPPER":` + jsonText(t, v) + `}}`
}

func pwSettings(t *testing.T, v string) string {
	return `{"processWrapper":` + jsonText(t, v) + `}`
}

// resolveFrame sends one launcher.resolve for cliPath and returns the reply frame.
func resolveFrame(t *testing.T, s *server, cliPath string) string {
	t.Helper()
	return dispatchRaw(t, s, authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":{"cliPath":`+jsonText(t, cliPath)+`}}`))
}

// quoteList is the JSON array of plain ASCII strings, as the frame writes it.
func quoteList(items ...string) string {
	return `["` + strings.Join(items, `","`) + `"]`
}

func usableFrame(source string, argv ...string) string {
	return `{"jsonrpc":"2.0","id":1,"result":{"status":"usable","argv":` + quoteList(argv...) + `,"source":"` + source + `"}}`
}

func unusableFrame(source, reason string) string {
	return `{"jsonrpc":"2.0","id":1,"result":{"status":"unusable","source":"` + source + `","reason":"` + reason + `"}}`
}

func unreadableFrame(path, reason string) string {
	return `{"jsonrpc":"2.0","id":1,"result":{"status":"unreadable","reason":"` + reason + `","path":"` + path + `"}}`
}

const noneFrame = `{"jsonrpc":"2.0","id":1,"result":{"status":"none"}}`

// checkResolve asserts one resolve frame.
func checkResolve(t *testing.T, s *server, row, cliPath, want string) {
	t.Helper()
	if got := resolveFrame(t, s, cliPath); got != want {
		t.Errorf("%s:\n got %s\nwant %s", row, got, want)
	}
}

// TestLauncherResolveNoneShapes pins R4 (RES-9): an absent or empty folder, and a
// base file of {}, of zero bytes or of white space only, answer none with no log.
func TestLauncherResolveNoneShapes(t *testing.T) {
	logs := captureLogBuf(t)
	s := newTestServer(t)
	dir := tempManagedSettingsDir(t)
	t.Setenv(managedSettingsDirEnv, filepath.Join(dir, "absent"))
	checkResolve(t, s, "R4-absent", "/opt/claude/cli", noneFrame)
	t.Setenv(managedSettingsDirEnv, dir)
	checkResolve(t, s, "R4-emptyfolder", "/opt/claude/cli", noneFrame)
	base := filepath.Join(dir, managedSettingsBase)
	for row, text := range map[string]string{"R4-base{}": "{}", "R4-baseempty": "", "R4-basews": "  \n\t \n"} {
		writeFileMode(t, base, []byte(text), 0o644)
		checkResolve(t, s, row, "/opt/claude/cli", noneFrame)
	}
	if strings.Contains(logs.String(), "[LauncherHandler]") {
		t.Errorf("a none answer must not log:\n%s", logs.String())
	}
}

// TestLauncherResolveKeysAndPrecedence pins R5, R6b, R6c and R6d: the env key beats
// processWrapper in one file, in both key orders; an env value of "" counts as
// unset; three spaces count as set with no launcher (T1), with a log line.
func TestLauncherResolveKeysAndPrecedence(t *testing.T) {
	logs := captureLogBuf(t)
	s := newTestServer(t)
	dir := tempManagedSettingsDir(t)
	w := mkExec(t, filepath.Join(dir, "bin", "wrap"))
	base := filepath.Join(dir, managedSettingsBase)

	writeFileMode(t, base, []byte(envSettings(t, w+` --x "a b"`)), 0o644)
	checkResolve(t, s, "R5", "/opt/claude/cli", usableFrame(base, w, "--x", "a b"))
	writeFileMode(t, base, []byte(pwSettings(t, w+` --x "a b"`)), 0o644)
	checkResolve(t, s, "R5 (processWrapper)", "/opt/claude/cli", usableFrame(base, w, "--x", "a b"))

	writeFileMode(t, base, []byte(`{"processWrapper":`+jsonText(t, w+" p")+`,"env":{"CLAUDE_CODE_PROCESS_WRAPPER":`+jsonText(t, w+" e")+`}}`), 0o644)
	checkResolve(t, s, "R6b-pw-first", "/opt/claude/cli", usableFrame(base, w, "e"))
	writeFileMode(t, base, []byte(`{"env":{"CLAUDE_CODE_PROCESS_WRAPPER":`+jsonText(t, w+" e")+`},"processWrapper":`+jsonText(t, w+" p")+`}`), 0o644)
	checkResolve(t, s, "R6b-env-first", "/opt/claude/cli", usableFrame(base, w, "e"))

	writeFileMode(t, base, []byte(`{"processWrapper":`+jsonText(t, w)+`,"env":{"CLAUDE_CODE_PROCESS_WRAPPER":""}}`), 0o644)
	checkResolve(t, s, "R6c", "/opt/claude/cli", usableFrame(base, w))

	t1 := managedNoLauncher
	writeFileMode(t, base, []byte(envSettings(t, "   ")), 0o644)
	checkResolve(t, s, "R6d-env3sp", "/opt/claude/cli", unusableFrame(base, t1))
	writeFileMode(t, base, []byte(`{"processWrapper":`+jsonText(t, w)+`,"env":{"CLAUDE_CODE_PROCESS_WRAPPER":"   "}}`), 0o644)
	checkResolve(t, s, "R6d-env3sp+pw", "/opt/claude/cli", unusableFrame(base, t1))
	if want := "[LauncherHandler] managed launcher from " + base + " refused: " + t1 + "\n"; !strings.Contains(logs.String(), want) {
		t.Errorf("no refusal line %q in:\n%s", want, logs.String())
	}
}

// TestLauncherResolveDropIns pins R7a-R7e, R8, R12f and R12g: drop-ins in name order
// win over the base file; the name filter; folder and FIFO entries are skipped;
// symlinks are followed; one unreadable file makes the answer unreadable; a later
// wrong-type value is skipped; a listing failure names the folder; a regular file
// named managed-settings.d is ignored.
func TestLauncherResolveDropIns(t *testing.T) {
	s := newTestServer(t)
	dir := tempManagedSettingsDir(t)
	w := mkExec(t, filepath.Join(dir, "bin", "wrap"))
	base := filepath.Join(dir, managedSettingsBase)
	dropDir := filepath.Join(dir, managedSettingsDropIns)
	reset := func() {
		t.Helper()
		_ = os.Remove(base)
		if err := os.RemoveAll(dropDir); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dropDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	reset()
	writeFileMode(t, base, []byte(pwSettings(t, w+" a")), 0o644)
	writeFileMode(t, filepath.Join(dropDir, "10.json"), []byte(pwSettings(t, w+" b")), 0o644)
	writeFileMode(t, filepath.Join(dropDir, "20.json"), []byte(pwSettings(t, w+" c")), 0o644)
	checkResolve(t, s, "R7a", "/opt/claude/cli", usableFrame(filepath.Join(dropDir, "20.json"), w, "c"))

	reset()
	writeFileMode(t, filepath.Join(dropDir, ".hidden.json"), []byte(pwSettings(t, w+" h")), 0o644)
	writeFileMode(t, filepath.Join(dropDir, "x.JSON"), []byte(pwSettings(t, w+" X")), 0o644)
	writeFileMode(t, filepath.Join(dropDir, "a.js"), []byte(pwSettings(t, w+" js")), 0o644)
	if err := os.Mkdir(filepath.Join(dropDir, "d.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A FIFO with no writer. An open of it blocks this test, which is the point.
	if err := syscall.Mkfifo(filepath.Join(dropDir, "p.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	check := fifoWatchdog(t, filepath.Join(dropDir, "p.json"))
	checkResolve(t, s, "R7b-all-five", "/opt/claude/cli", noneFrame)
	check()

	reset()
	target := filepath.Join(dir, "target.json")
	writeFileMode(t, target, []byte(pwSettings(t, w+" s")), 0o644)
	if err := os.Symlink(target, filepath.Join(dropDir, "s.json")); err != nil {
		t.Fatal(err)
	}
	checkResolve(t, s, "R7c-s-only", "/opt/claude/cli", usableFrame(filepath.Join(dropDir, "s.json"), w, "s"))
	if err := os.Symlink(filepath.Join(dir, "nosuch.json"), filepath.Join(dropDir, "z.json")); err != nil {
		t.Fatal(err)
	}
	checkResolve(t, s, "R7c-both", "/opt/claude/cli", usableFrame(filepath.Join(dropDir, "s.json"), w, "s"))
	if err := os.Remove(filepath.Join(dropDir, "s.json")); err != nil {
		t.Fatal(err)
	}
	checkResolve(t, s, "R7c-z-only", "/opt/claude/cli", noneFrame)

	reset()
	somedir := filepath.Join(dir, "somedir")
	if err := os.Mkdir(somedir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(somedir, filepath.Join(dropDir, "l.json")); err != nil {
		t.Fatal(err)
	}
	checkResolve(t, s, "R7d", "/opt/claude/cli", unreadableFrame(filepath.Join(dropDir, "l.json"), "is a directory"))

	reset()
	writeFileMode(t, base, []byte(pwSettings(t, w+" a")), 0o644)
	writeFileMode(t, filepath.Join(dropDir, "20.json"), []byte("{"), 0o644)
	checkResolve(t, s, "R7e", "/opt/claude/cli", unreadableFrame(filepath.Join(dropDir, "20.json"), "not valid JSON"))

	for row, later := range map[string]string{
		"R8-pw5":         `{"processWrapper":5}`,
		"R8-env-x":       `{"env":"x"}`,
		"R8-envval-true": `{"env":{"CLAUDE_CODE_PROCESS_WRAPPER":true}}`,
	} {
		reset()
		writeFileMode(t, filepath.Join(dropDir, "10.json"), []byte(pwSettings(t, w)), 0o644)
		writeFileMode(t, filepath.Join(dropDir, "20.json"), []byte(later), 0o644)
		checkResolve(t, s, row, "/opt/claude/cli", usableFrame(filepath.Join(dropDir, "10.json"), w))
	}

	reset()
	writeFileMode(t, base, []byte(pwSettings(t, w+" a")), 0o644)
	writeFileMode(t, filepath.Join(dropDir, "10.json"), []byte(pwSettings(t, w+" b")), 0o644)
	old := readManagedDropInDir
	readManagedDropInDir = func(d string) ([]fs.DirEntry, error) {
		return nil, &fs.PathError{Op: "open", Path: d, Err: syscall.EACCES}
	}
	checkResolve(t, s, "R12f", "/opt/claude/cli", unreadableFrame(dropDir, "the drop-in directory could not be listed: permission denied"))
	readManagedDropInDir = old

	reset()
	if err := os.RemoveAll(dropDir); err != nil {
		t.Fatal(err)
	}
	writeFileMode(t, base, []byte(pwSettings(t, w+" a")), 0o644)
	writeFileMode(t, dropDir, []byte(pwSettings(t, w+" d")), 0o644)
	checkResolve(t, s, "R12g", "/opt/claude/cli", usableFrame(base, w, "a"))
}

// TestLauncherResolveEscapesAndRawBytes pins RES-25 on the wire: R9g escapes `<`,
// `&`, `>` and U+2028; R10i keeps U+FFFD raw (ef bf bd); R10g-0085 keeps U+0085 raw
// (c2 85) inside the T11 reason; R9c's em dash stays raw.
func TestLauncherResolveEscapesAndRawBytes(t *testing.T) {
	s := newTestServer(t)
	dir := tempManagedSettingsDir(t)
	w := mkExec(t, filepath.Join(dir, "bin", "wrap"))
	base := filepath.Join(dir, managedSettingsBase)

	// The array text holds the JSON escape \u2028, so the settings file holds \\u2028.
	writeFileMode(t, base, []byte(`{"env":{"CLAUDE_CODE_PROCESS_WRAPPER":"[\"`+w+`\",\"<a&b>\",\"x\\u2028y\"]"}}`), 0o644)
	checkResolve(t, s, "R9g", "/opt/claude/cli",
		`{"jsonrpc":"2.0","id":1,"result":{"status":"usable","argv":["`+w+`","\u003ca\u0026b\u003e","x\u2028y"],"source":"`+base+`"}}`)

	writeFileMode(t, base, []byte(`{"processWrapper":"`+w+" a\xff\"}"), 0o644)
	checkResolve(t, s, "R10i", "/opt/claude/cli",
		`{"jsonrpc":"2.0","id":1,"result":{"status":"usable","argv":["`+w+`","a`+"\xef\xbf\xbd"+`"],"source":"`+base+`"}}`)

	writeFileMode(t, base, []byte(`{"processWrapper":"`+w+"\xc2\x85"+`a"}`), 0o644)
	checkResolve(t, s, "R10g-0085", "/opt/claude/cli",
		unusableFrame(base, "launcher `"+w+"\xc2\x85"+"a` does not exist or is not an executable regular file"))

	writeFileMode(t, base, []byte(envSettings(t, `["`+w+`",""]`)), 0o644)
	checkResolve(t, s, "R9c", "/opt/claude/cli",
		unusableFrame(base, "the JSON array contains an empty element \xe2\x80\x94 remove it, or fill in the value it was a placeholder for"))

	writeFileMode(t, base, []byte(envSettings(t, w+" a;")), 0o644)
	checkResolve(t, s, "R10a", "/opt/claude/cli",
		unusableFrame(base, "the value contains an unquoted shell metacharacter (one of ; | \\u0026 $ ( ) ` \\u003c \\u003e) \xe2\x80\x94 it is an argv list, not a shell command"))

	writeFileMode(t, base, []byte(envSettings(t, w+` ""`)), 0o644)
	checkResolve(t, s, "R10h-1", "/opt/claude/cli",
		unusableFrame(base, "the value contains an empty `\\\"\\\"` token \xe2\x80\x94 remove it, or fill in the value it was a placeholder for"))
}

// TestLauncherResolveLauncherChecks pins R11a-R11e: T8, T9 (a string compare that
// runs before the absolute and existence checks), T10 (case-sensitive), T11 (missing,
// folder, 0644), a symlink kept as its link path, and the long-path cut.
func TestLauncherResolveLauncherChecks(t *testing.T) {
	s := newTestServer(t)
	dir := tempManagedSettingsDir(t)
	base := filepath.Join(dir, managedSettingsBase)
	bin := filepath.Join(dir, "bin")
	w := mkExec(t, filepath.Join(bin, "wrap"))
	set := func(v string) { t.Helper(); writeFileMode(t, base, []byte(envSettings(t, v)), 0o644) }

	set("wrap")
	checkResolve(t, s, "R11a", "/opt/claude/cli", unusableFrame(base, managedNotAbsolute))

	cli := mkExec(t, filepath.Join(bin, "cli"))
	set(cli)
	checkResolve(t, s, "R11b-1", cli, unusableFrame(base, "launcher `"+cli+"` is Claude Code's own path"))
	set("/opt/claude/cli")
	checkResolve(t, s, "R11b-2", "/opt/claude/cli", unusableFrame(base, "launcher `/opt/claude/cli` is Claude Code's own path"))
	set("c")
	checkResolve(t, s, "R11b-3", "c", unusableFrame(base, "launcher `c` is Claude Code's own path"))

	for _, name := range []string{"x.js", "x.mjs", "x.ts", "x.tsx", "x.jsx"} {
		p := mkExec(t, filepath.Join(bin, name))
		set(p)
		checkResolve(t, s, "R11c-"+name, "/opt/claude/cli", unusableFrame(base, "launcher `"+p+"` is a script the SDK would run in place of Claude Code"))
	}
	upper := mkExec(t, filepath.Join(bin, "X.JS"))
	set(upper)
	checkResolve(t, s, "R11c-X.JS", "/opt/claude/cli", usableFrame(base, upper))

	missing := filepath.Join(bin, "missing")
	folder := filepath.Join(dir, "dir")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	w644 := filepath.Join(bin, "w644")
	writeFileMode(t, w644, []byte("#\n"), 0o644)
	for _, p := range []string{missing, folder, w644} {
		set(p)
		checkResolve(t, s, "R11d "+filepath.Base(p), "/opt/claude/cli", unusableFrame(base, "launcher `"+p+"` does not exist or is not an executable regular file"))
	}
	link := filepath.Join(bin, "wlink")
	if err := os.Symlink(w, link); err != nil {
		t.Fatal(err)
	}
	set(link)
	checkResolve(t, s, "R11d-symlink", "/opt/claude/cli", usableFrame(base, link))

	// R11e: a 300-byte launcher path with a 3-byte character at bytes 254-256.
	prefix := filepath.Join(bin, "L") + "/"
	fill := 254 - len(prefix)
	var parts []string
	for fill > 0 {
		n := min(fill, 100)
		if fill-n == 0 {
			parts = append(parts, strings.Repeat("x", n))
		} else {
			parts = append(parts, strings.Repeat("d", n-1)+"/")
		}
		fill -= n
	}
	head := prefix + strings.Join(parts, "")
	if len(head) != 254 {
		t.Fatalf("long-path head is %d bytes, want 254", len(head))
	}
	exists := head + "\u20ac" + strings.Repeat("y", 43)
	if len(exists) != 300 {
		t.Fatalf("long path is %d bytes, want 300", len(exists))
	}
	mkExec(t, exists)
	set(exists)
	checkResolve(t, s, "R11e-exists", "/opt/claude/cli", usableFrame(base, exists))
	set(exists[:len(exists)-1] + "z")
	checkResolve(t, s, "R11e-missing", "/opt/claude/cli", unusableFrame(base, "launcher `"+head+"\xe2\x80\xa6` does not exist or is not an executable regular file"))
}

// TestLauncherResolveBaseFileChecks pins R12a-R12e: permission denied (through the
// read seam), a folder, a FIFO that is not opened, the 2 MiB limit on both sides,
// invalid JSON, a non-object, and the byte order marks.
func TestLauncherResolveBaseFileChecks(t *testing.T) {
	s := newTestServer(t)
	dir := tempManagedSettingsDir(t)
	base := filepath.Join(dir, managedSettingsBase)
	w := mkExec(t, filepath.Join(dir, "bin", "wrap"))
	doc := pwSettings(t, w)

	writeFileMode(t, base, []byte(doc), 0o644)
	old := readManagedSettingsFile
	readManagedSettingsFile = func(p string) ([]byte, error) {
		return nil, &fs.PathError{Op: "open", Path: p, Err: syscall.EACCES}
	}
	checkResolve(t, s, "R12a", "/opt/claude/cli", unreadableFrame(base, "permission denied"))
	readManagedSettingsFile = old

	_ = os.Remove(base)
	if err := os.Mkdir(base, 0o755); err != nil {
		t.Fatal(err)
	}
	checkResolve(t, s, "R12b-folder", "/opt/claude/cli", unreadableFrame(base, "is a directory"))
	_ = os.Remove(base)
	if err := syscall.Mkfifo(base, 0o644); err != nil {
		t.Fatal(err)
	}
	check := fifoWatchdog(t, base)
	checkResolve(t, s, "R12b-fifo", "/opt/claude/cli", unreadableFrame(base, "not a regular file"))
	check()
	_ = os.Remove(base)

	pad := func(n int) []byte { return []byte(doc + strings.Repeat(" ", n-len(doc))) }
	writeFileMode(t, base, pad(2097153), 0o644)
	checkResolve(t, s, "R12c-2097153", "/opt/claude/cli", unreadableFrame(base, "larger than 2 MiB"))
	writeFileMode(t, base, pad(2097152), 0o644)
	checkResolve(t, s, "R12c-2097152", "/opt/claude/cli", usableFrame(base, w))

	for row, text := range map[string]string{"R12d-brace": "{", "R12d-array": "[]", "R12d-null": "null"} {
		writeFileMode(t, base, []byte(text), 0o644)
		want := "not a JSON object"
		if text == "{" {
			want = "not valid JSON"
		}
		checkResolve(t, s, row, "/opt/claude/cli", unreadableFrame(base, want))
	}

	utf16Of := func(le bool) []byte {
		var b []byte
		for _, u := range utf16.Encode([]rune(doc)) {
			if le {
				b = append(b, byte(u), byte(u>>8))
			} else {
				b = append(b, byte(u>>8), byte(u))
			}
		}
		return b
	}
	writeFileMode(t, base, append([]byte{0xef, 0xbb, 0xbf}, doc...), 0o644)
	checkResolve(t, s, "R12e-utf8bom", "/opt/claude/cli", usableFrame(base, w))
	writeFileMode(t, base, append([]byte{0xff, 0xfe}, utf16Of(true)...), 0o644)
	checkResolve(t, s, "R12e-utf16le", "/opt/claude/cli", usableFrame(base, w))
	writeFileMode(t, base, append([]byte{0xfe, 0xff}, utf16Of(false)...), 0o644)
	checkResolve(t, s, "R12e-utf16be", "/opt/claude/cli", unreadableFrame(base, "not valid JSON"))
	writeFileMode(t, base, append([]byte{0xc2, 0xa0}, doc...), 0o644)
	checkResolve(t, s, "R12e-nbsp", "/opt/claude/cli", unreadableFrame(base, "not valid JSON"))
}

// TestLauncherResolveNoCacheRunsNothing pins R13 and R6a-lw: an edit shows on the
// next ask, a removed folder answers none at once, and resolve never runs the
// launcher.
func TestLauncherResolveNoCacheRunsNothing(t *testing.T) {
	s := newTestServer(t)
	dir := tempManagedSettingsDir(t)
	base := filepath.Join(dir, managedSettingsBase)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	lw := filepath.Join(dir, "bin", "lw")
	if err := os.MkdirAll(filepath.Dir(lw), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, lw); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "launch.log")
	t.Setenv("CLAUSTRUM_TEST_HELPER", "wrap")
	t.Setenv("CLAUSTRUM_TEST_WRAP_LOG", marker)

	writeFileMode(t, base, []byte(envSettings(t, lw+` --x "a b"`)), 0o644)
	for i := 0; i < 3; i++ {
		checkResolve(t, s, "R13 ask", "/opt/claude/cli", usableFrame(base, lw, "--x", "a b"))
	}
	writeFileMode(t, base, []byte(pwSettings(t, lw+" changed")), 0o644)
	checkResolve(t, s, "R13 after edit", "/opt/claude/cli", usableFrame(base, lw, "changed"))
	if err := os.Remove(base); err != nil {
		t.Fatal(err)
	}
	checkResolve(t, s, "R13 after removal", "/opt/claude/cli", noneFrame)
	if _, err := os.Stat(marker); err == nil {
		t.Error("resolve ran the launcher (R6a-lw: it must not)")
	}
}

// TestLauncherResolveLogDedup pins R14 on one daemon: asks 1-3 unusable (T8), ask 4
// usable, ask 5 T8 again, ask 6 unreadable JSON, ask 7 permission denied. Lines
// appear on asks 1, 5, 6 and 7.
func TestLauncherResolveLogDedup(t *testing.T) {
	logs := captureLogBuf(t)
	s := newTestServer(t)
	dir := tempManagedSettingsDir(t)
	base := filepath.Join(dir, managedSettingsBase)
	w := mkExec(t, filepath.Join(dir, "bin", "wrap"))
	t8 := "[LauncherHandler] managed launcher from " + base + " refused: " + managedNotAbsolute
	lines := func() int { return strings.Count(logs.String(), "[LauncherHandler]") }

	steps := []struct {
		setup func()
		lines int
	}{
		{func() { writeFileMode(t, base, []byte(pwSettings(t, "wrap")), 0o644) }, 1},
		{func() {}, 1},
		{func() {}, 1},
		{func() { writeFileMode(t, base, []byte(pwSettings(t, w)), 0o644) }, 1},
		{func() { writeFileMode(t, base, []byte(pwSettings(t, "wrap")), 0o644) }, 2},
		{func() { writeFileMode(t, base, []byte("{"), 0o644) }, 3},
		{func() {
			old := readManagedSettingsFile
			readManagedSettingsFile = func(p string) ([]byte, error) {
				return nil, &fs.PathError{Op: "open", Path: p, Err: syscall.EACCES}
			}
			t.Cleanup(func() { readManagedSettingsFile = old })
		}, 4},
	}
	for i, st := range steps {
		st.setup()
		resolveFrame(t, s, "/opt/claude/cli")
		if got := lines(); got != st.lines {
			t.Fatalf("after ask %d: %d [LauncherHandler] lines, want %d:\n%s", i+1, got, st.lines, logs.String())
		}
	}
	out := logs.String()
	if strings.Count(out, t8+"\n") != 2 {
		t.Errorf("want the T8 line twice (asks 1 and 5):\n%s", out)
	}
	for _, want := range []string{
		"[LauncherHandler] managed settings unreadable: " + base + ": not valid JSON\n",
		"[LauncherHandler] managed settings unreadable: " + base + ": permission denied\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// wrapLauncherFixture makes <tmp>/bin/wrap, a symlink to this test binary, so the
// launcher path differs from the command path (the test binary itself), and returns
// both.
func wrapLauncherFixture(t *testing.T) (wrap, exe string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrap = filepath.Join(t.TempDir(), "bin", "wrap")
	if err := os.MkdirAll(filepath.Dir(wrap), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, wrap); err != nil {
		t.Fatal(err)
	}
	return wrap, exe
}

// launchedSpawnReq is one authenticated process.spawn request with id 1.
func launchedSpawnReq(t *testing.T, params map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "process.spawn", "auth": testToken, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func spawnErrFrame(code, msg string) string {
	return `{"jsonrpc":"2.0","id":1,"error":{"code":` + code + `,"message":"` + msg + `"}}`
}

func exitCodeOf(frames []streamFrame) int {
	for _, f := range frames {
		if f.Stream == "exit" && f.ExitCode != nil {
			return *f.ExitCode
		}
	}
	return -999
}

// TestSpawnThroughLauncher pins S0, S1, S4 and S9 on a live socket: the child argv
// is the launcher argv, then the command, then the args; the launcher's streams
// are the process's own; a null or absent launcher spawns the command itself; a
// null element is an empty arg. It also pins the "via launcher" log line.
func TestSpawnThroughLauncher(t *testing.T) {
	logs := captureLogBuf(t)
	wrap, exe := wrapLauncherFixture(t)
	_, sock := newRunningServer(t)
	cl := dial(t, sock)
	env := map[string]string{"CLAUSTRUM_TEST_HELPER": "wrap", "CLAUSTRUM_TEST_WRAP_NEXT": "echo"}

	if got := string(cl.call(launchedSpawnReq(t, map[string]any{"id": "p0", "command": exe, "args": []string{"hi"}, "env": env, "launcher": []string{wrap}}))); got != `{"jsonrpc":"2.0","id":1,"result":{"success":true}}` {
		t.Fatalf("S0 reply %s", got)
	}
	fr := cl.waitExit("p0")
	if got, want := streamBytes(t, fr, "stderr"), "WRAP:"+exe+" hi\n"; got != want {
		t.Errorf("S0 stderr %q, want %q", got, want)
	}
	if got := streamBytes(t, fr, "stdout"); got != "hi\n" {
		t.Errorf("S0 stdout %q, want %q", got, "hi\n")
	}
	if c := exitCodeOf(fr); c != 0 {
		t.Errorf("S0 exit %d, want 0", c)
	}
	if want := ", command=" + exe + " via launcher " + wrap + "\n"; !strings.Contains(logs.String(), "[process.Manager] Process p0 started, PID=") || !strings.Contains(logs.String(), want) {
		t.Errorf("no started line ending %q in:\n%s", want, logs.String())
	}

	cl.call(launchedSpawnReq(t, map[string]any{"id": "p1", "command": exe, "args": []string{"hi"}, "env": env, "launcher": []string{wrap, "-a"}}))
	if got, want := streamBytes(t, cl.waitExit("p1"), "stderr"), "WRAP:-a "+exe+" hi\n"; !strings.HasPrefix(got, want) {
		t.Errorf("S1 stderr %q, want it to start %q", got, want)
	}

	cl.call(launchedSpawnReq(t, map[string]any{"id": "p9", "command": exe, "args": []string{"hi"}, "env": env, "launcher": []any{wrap, nil, "-a"}}))
	if got, want := streamBytes(t, cl.waitExit("p9"), "stderr"), "WRAP: -a "+exe+" hi\n"; !strings.HasPrefix(got, want) {
		t.Errorf("S9 stderr %q, want it to start %q", got, want)
	}

	echo, echoEnv := helperCommand(t, "echo")
	for _, id := range []string{"p4null", "p4absent"} {
		params := map[string]any{"id": id, "command": echo, "args": []string{"hi"}, "env": echoEnv}
		if id == "p4null" {
			params["launcher"] = nil
		}
		cl.call(launchedSpawnReq(t, params))
		fr := cl.waitExit(id)
		if got := streamBytes(t, fr, "stdout") + "|" + streamBytes(t, fr, "stderr"); got != "hi\n|" {
			t.Errorf("S4 %s: stdout|stderr %q, want %q", id, got, "hi\n|")
		}
	}
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "Process p4null started") && !strings.HasSuffix(line, ", command="+echo) {
			t.Errorf("S4: a null launcher logs the plain started line, got %q", line)
		}
	}
}

// TestSpawnLauncherRefusals pins S2, S3, S5 and S11: the relative-command check
// (-32602) runs before any launcher check; the launcher checks answer -32004 with
// the T texts, an empty launcher T1; both come before the cwd check, and a good
// launcher meets the cwd check with the plain frame. Each refusal logs one
// [process.Manager] line and no [LauncherHandler] line.
func TestSpawnLauncherRefusals(t *testing.T) {
	logs := captureLogBuf(t)
	s := newTestServer(t)
	wrap, exe := wrapLauncherFixture(t)
	tmp := t.TempDir()
	xjs := mkExec(t, filepath.Join(tmp, "bin", "x.js"))
	nope := filepath.Join(tmp, "nope")
	nocwd := filepath.Join(tmp, "nocwd")
	cannot := "the managed launcher cannot be used: "

	cases := []struct {
		row    string
		params map[string]any
		want   string
	}{
		{"S2-echo-W", map[string]any{"id": "p1", "command": "echo", "args": []string{"hi"}, "launcher": []string{wrap}}, spawnErrFrame("-32602", managedSpawnRelative)},
		{"S2-echo-empty", map[string]any{"id": "p1", "command": "echo", "args": []string{"hi"}, "launcher": []string{}}, spawnErrFrame("-32602", managedSpawnRelative)},
		{"S11-relcmd-badlauncher", map[string]any{"id": "p1", "command": "echo", "args": []string{"hi"}, "launcher": []string{nope}}, spawnErrFrame("-32602", managedSpawnRelative)},
		{"S3", map[string]any{"id": "p1", "command": exe, "args": []string{"hi"}, "launcher": []string{}}, spawnErrFrame("-32004", cannot+"the value is set but contains no launcher \xe2\x80\x94 unset it to run without one, or set it to the absolute path of your launcher")},
		{"S5-wrap", map[string]any{"id": "p1", "command": exe, "args": []string{"hi"}, "launcher": []string{"wrap"}}, spawnErrFrame("-32004", cannot+managedNotAbsolute)},
		{"S5-own-path", map[string]any{"id": "p1", "command": exe, "args": []string{"hi"}, "launcher": []string{exe}}, spawnErrFrame("-32004", cannot+"launcher `"+exe+"` is Claude Code's own path")},
		{"S5-x.js", map[string]any{"id": "p1", "command": exe, "args": []string{"hi"}, "launcher": []string{xjs}}, spawnErrFrame("-32004", cannot+"launcher `"+xjs+"` is a script the SDK would run in place of Claude Code")},
		{"S5-nope", map[string]any{"id": "p1", "command": exe, "args": []string{"hi"}, "launcher": []string{nope}}, spawnErrFrame("-32004", cannot+"launcher `"+nope+"` does not exist or is not an executable regular file")},
		{"S11-badlauncher-nocwd", map[string]any{"id": "p1", "command": exe, "args": []string{"hi"}, "cwd": nocwd, "launcher": []string{nope}}, spawnErrFrame("-32004", cannot+"launcher `"+nope+"` does not exist or is not an executable regular file")},
		{"S11-goodlauncher-nocwd", map[string]any{"id": "p1", "command": exe, "args": []string{"hi"}, "cwd": nocwd, "launcher": []string{wrap}}, spawnErrFrame("-32603", "chdir "+nocwd+": stat "+nocwd+": no such file or directory")},
	}
	for _, c := range cases {
		if got := dispatchRaw(t, s, launchedSpawnReq(t, c.params)); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.row, got, c.want)
		}
	}
	out := logs.String()
	for _, want := range []string{
		"[process.Manager] Failed to start process p1: " + managedSpawnRelative + "\n",
		"[process.Manager] Failed to start process p1: " + cannot + managedNotAbsolute + "\n",
		"[process.Manager] Failed to start process p1: " + cannot + managedNoLauncher + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing log %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "[LauncherHandler]") {
		t.Errorf("a spawn refusal must not write a [LauncherHandler] line (RES-29b):\n%s", out)
	}
}

// TestSpawnLauncherCannotStart pins S6a, S6b and S6c: a launcher that passes the
// checks but does not start is -32005, on a bare socket and through the exec-child
// trampoline of a run-shaped socket.
func TestSpawnLauncherCannotStart(t *testing.T) {
	_, exe := wrapLauncherFixture(t)
	bin := filepath.Join(t.TempDir(), "bin")
	s6a := filepath.Join(bin, "s6a")
	writeFileMode(t, s6a, []byte("#!/nonexistent/sh\necho hi\n"), 0o755)
	s6b := filepath.Join(bin, "s6b")
	writeFileMode(t, s6b, []byte("echo hi\n"), 0o755)
	s6c := filepath.Join(bin, "s6c")
	writeFileMode(t, s6c, []byte("#!"+exe+"\r\necho hi\n"), 0o755)
	interp := "its interpreter was not found (the program on its #! line, or the loader of an ELF binary; a script saved with Windows CRLF line endings fails this way)"
	want := map[string]string{
		s6a: spawnErrFrame("-32005", "the managed launcher "+s6a+" could not be started: "+interp),
		s6b: spawnErrFrame("-32005", "the managed launcher "+s6b+" could not be started: exec format error"),
		s6c: spawnErrFrame("-32005", "the managed launcher "+s6c+" could not be started: "+interp),
	}

	base, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	runDir := filepath.Join(base, "run", "c1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, bare := newRunningServer(t)
	s, shaped := newRunningServerAt(t, filepath.Join(runDir, "rpc.sock"))
	if s.procs.runDir == "" {
		t.Fatal("the run-shaped socket did not enable the trampoline")
	}
	for name, sock := range map[string]string{"bare": bare, "trampoline": shaped} {
		cl := dial(t, sock)
		for launcher, w := range want {
			got := string(cl.call(launchedSpawnReq(t, map[string]any{"id": "p1", "command": exe, "args": []string{"hi"}, "launcher": []string{launcher}})))
			if got != w {
				t.Errorf("%s %s:\n got %s\nwant %s", name, filepath.Base(launcher), got, w)
			}
		}
	}
}

// TestSpawnLauncherBadCommand pins S7: a command that does not run gives the same
// -32603 frame as a spawn without a launcher, and the launcher does not run.
func TestSpawnLauncherBadCommand(t *testing.T) {
	s := newTestServer(t)
	wrap, _ := wrapLauncherFixture(t)
	tmp := t.TempDir()
	marker := filepath.Join(tmp, "launch.log")
	folder := filepath.Join(tmp, "dir")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	c644 := filepath.Join(tmp, "c644")
	writeFileMode(t, c644, []byte("#\n"), 0o644)
	nope := filepath.Join(tmp, "nope")
	env := map[string]string{"CLAUSTRUM_TEST_HELPER": "wrap", "CLAUSTRUM_TEST_WRAP_LOG": marker}
	for cmd, reason := range map[string]string{nope: "no such file or directory", folder: "permission denied", c644: "permission denied"} {
		want := spawnErrFrame("-32603", "fork/exec "+cmd+": "+reason)
		if got := dispatchRaw(t, s, launchedSpawnReq(t, map[string]any{"id": "p1", "command": cmd, "args": []string{"hi"}, "env": env, "launcher": []string{wrap}})); got != want {
			t.Errorf("S7 %s with launcher:\n got %s\nwant %s", filepath.Base(cmd), got, want)
		}
		if got := dispatchRaw(t, s, launchedSpawnReq(t, map[string]any{"id": "p1", "command": cmd, "args": []string{"hi"}})); got != want {
			t.Errorf("S7 %s without launcher (the control):\n got %s\nwant %s", filepath.Base(cmd), got, want)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the launcher ran for a command that cannot run")
	}
}

// TestSpawnStripsLauncherEnv pins S8 (ENV-1 to ENV-3): every child loses the gate
// and the E2E variable; a launched child also loses CLAUDE_CODE_PROCESS_WRAPPER;
// the strip covers the daemon env and the spawn env param.
func TestSpawnStripsLauncherEnv(t *testing.T) {
	t.Setenv(managedLauncherGateEnv, "1")
	t.Setenv(managedSettingsDirEnv, "x")
	t.Setenv(managedWrapperEnv, "y")
	wrap, exe := wrapLauncherFixture(t)
	_, sock := newRunningServer(t)
	cl := dial(t, sock)
	names := []string{managedLauncherGateEnv, managedSettingsDirEnv, managedWrapperEnv}
	out := func(gate, dir, wrapper string) string {
		return managedLauncherGateEnv + "=" + gate + "\n" + managedSettingsDirEnv + "=" + dir + "\n" + managedWrapperEnv + "=" + wrapper + "\n"
	}
	spawnEnv := map[string]string{managedLauncherGateEnv: "s1", managedSettingsDirEnv: "s2", managedWrapperEnv: "s3"}
	cases := []struct {
		row      string
		launched bool
		env      map[string]string
		want     string
	}{
		{"S8-nolauncher", false, nil, out("", "", "y")},
		{"S8-launcher", true, nil, out("", "", "")},
		{"S8-nolauncher-spawnenv", false, spawnEnv, out("", "", "s3")},
		{"S8-launcher-spawnenv", true, spawnEnv, out("", "", "")},
	}
	for _, c := range cases {
		env := map[string]string{"CLAUSTRUM_TEST_HELPER": "printenvs"}
		for k, v := range c.env {
			env[k] = v
		}
		params := map[string]any{"id": c.row, "command": exe, "args": names, "env": env}
		if c.launched {
			env["CLAUSTRUM_TEST_HELPER"], env["CLAUSTRUM_TEST_WRAP_NEXT"] = "wrap", "printenvs"
			params["launcher"] = []string{wrap}
		}
		cl.call(launchedSpawnReq(t, params))
		if got := streamBytes(t, cl.waitExit(c.row), "stdout"); got != c.want {
			t.Errorf("%s: child env\n%s\nwant\n%s", c.row, got, c.want)
		}
	}
}

// setManagedSystemDir points the system managed-settings folder at dir for one test.
func setManagedSystemDir(t *testing.T, dir string) {
	t.Helper()
	old := managedSystemSettingsDir
	managedSystemSettingsDir = dir
	t.Cleanup(func() { managedSystemSettingsDir = old })
}

// TestLauncherResolveSystemFolderFirst pins RX, RXe, RXs and RXd. RX: with the
// system folder absent, CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR selects its folder and
// nothing is logged. RXe: an empty system folder changes nothing. RXs: the base file
// in the system folder makes the resolve ignore the variable and read the system
// file. The `[launcher] … ignored: <system folder> holds this host's policy` line
// logs once per daemon, on the first of two resolves. RXd: a drop-in alone in the
// system folder counts too, on a fresh daemon. RXd2: a hidden .json entry alone
// counts as well, and the resolve then answers none.
func TestLauncherResolveSystemFolderFirst(t *testing.T) {
	logs := captureLogBuf(t)
	s := newTestServer(t)
	root := t.TempDir()
	sys := filepath.Join(root, "sys")
	w := mkExec(t, filepath.Join(root, "bin", "wrap"))
	varDir := tempManagedSettingsDir(t)
	setManagedSystemDir(t, sys) // after the fixture, which points the system folder elsewhere
	varBase := filepath.Join(varDir, managedSettingsBase)
	writeFileMode(t, varBase, []byte(envSettings(t, w+` --x "a b"`)), 0o644)
	sysBase := filepath.Join(sys, managedSettingsBase)
	ignored := "[launcher] CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR ignored: " + sys + " holds this host's policy\n"

	checkResolve(t, s, "RX", "/opt/claude/cli", usableFrame(varBase, w, "--x", "a b"))
	if strings.Contains(logs.String(), "[launcher]") {
		t.Errorf("RX must log no [launcher] line:\n%s", logs.String())
	}

	if err := os.Mkdir(sys, 0o755); err != nil {
		t.Fatal(err)
	}
	checkResolve(t, s, "RXe", "/opt/claude/cli", usableFrame(varBase, w, "--x", "a b"))
	if strings.Contains(logs.String(), "[launcher]") {
		t.Errorf("RXe: an empty system folder does not ignore the variable:\n%s", logs.String())
	}

	writeFileMode(t, sysBase, []byte(pwSettings(t, w+" sys")), 0o644)
	checkResolve(t, s, "RXs", "/opt/claude/cli", usableFrame(sysBase, w, "sys"))
	checkResolve(t, s, "RXs again", "/opt/claude/cli", usableFrame(sysBase, w, "sys"))
	if got := strings.Count(logs.String(), ignored); got != 1 {
		t.Errorf("RXs: %d lines %q in:\n%s\nwant one per daemon (1)", got, ignored, logs.String())
	}

	if err := os.Remove(sysBase); err != nil {
		t.Fatal(err)
	}
	dropIn := filepath.Join(sys, managedSettingsDropIns, "10.json")
	writeFileMode(t, dropIn, []byte(pwSettings(t, w+" d")), 0o644)
	s2 := newTestServer(t)
	checkResolve(t, s2, "RXd", "/opt/claude/cli", usableFrame(dropIn, w, "d"))
	if got := strings.Count(logs.String(), ignored); got != 2 {
		t.Errorf("RXd: the fresh daemon logs the line once more, got %d in:\n%s", got, logs.String())
	}

	// RXd2: only a hidden .json entry in the system managed-settings.d. It counts as
	// policy, so the variable is ignored, and the resolve skips it and answers none.
	if err := os.Remove(dropIn); err != nil {
		t.Fatal(err)
	}
	writeFileMode(t, filepath.Join(sys, managedSettingsDropIns, ".x.json"), []byte(pwSettings(t, w+" h")), 0o644)
	s3 := newTestServer(t)
	checkResolve(t, s3, "RXd2", "/opt/claude/cli", noneFrame)
	if got := strings.Count(logs.String(), ignored); got != 3 {
		t.Errorf("RXd2: the fresh daemon logs the line once more, got %d in:\n%s", got, logs.String())
	}
}

// TestManagedIgnoredVarLineBytes pins the measured text of the RXs log line, with
// the system folder of this OS (Linux and macOS texts both measured).
func TestManagedIgnoredVarLineBytes(t *testing.T) {
	want := map[string]string{
		"linux":  "[launcher] CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR ignored: /etc/claude-code holds this host's policy",
		"darwin": "[launcher] CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR ignored: /Library/Application Support/ClaudeCode holds this host's policy",
	}[runtime.GOOS]
	if want == "" {
		t.Skip("no measured text for " + runtime.GOOS)
	}
	if got := managedIgnoredVarLine(); got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
}
