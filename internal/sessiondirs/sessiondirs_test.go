package sessiondirs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a log sink that the tests read while the code writes to it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	old := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return buf
}

// gitFile is the file name of git on this system.
func gitFile() string {
	if runtime.GOOS == "windows" {
		return "git.exe"
	}
	return "git"
}

// binWithGit makes <dir>/bin with an executable git file that no test runs.
func binWithGit(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, gitFile()), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func recordDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), RecordFolderName)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseForTest(dir))
	return dir
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// A record is one file per folder in the record folder: the first 32 hex digits
// of the SHA-256 of the path as its name, mode 0600, and the body
// `<ms> <dev> <inode>\n<path>` with no newline at the end. Another file there
// stays.
func TestRememberWritesOneRecord(t *testing.T) {
	dir := recordDir(t)
	if err := os.WriteFile(filepath.Join(dir, "keep-me"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := t.TempDir()
	before := time.Now().UnixMilli()
	_ = Remember(s)
	sum := sha256.Sum256([]byte(s))
	name := hex.EncodeToString(sum[:])[:32]
	if got := names(t, dir); !slices.Equal(got, []string{name, "keep-me"}) && !slices.Equal(got, []string{"keep-me", name}) {
		t.Fatalf("the record folder holds %v, want %s and keep-me", got, name)
	}
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`^([0-9]+) ([0-9]+) ([0-9]+)\n(.*)$`).FindStringSubmatch(string(b))
	if m == nil || m[4] != s {
		t.Fatalf("the record holds %q, want `<ms> <dev> <inode>\\n%s`", b, s)
	}
	var ms int64
	_, _ = fmt.Sscan(m[1], &ms)
	if ms < before || ms > time.Now().UnixMilli() {
		t.Errorf("the record time %d is not the time of the write", ms)
	}
	if runtime.GOOS == "windows" && (m[2] != "0" || m[3] != "0") {
		t.Errorf("dev and inode are %s %s on Windows, want 0 0", m[2], m[3])
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("the record mode: %v %v, want 0600", fi.Mode(), err)
		}
	}
	// A folder that the daemon knows gets no second record.
	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
	_ = Remember(s)
	if got := names(t, dir); !slices.Equal(got, []string{"keep-me"}) {
		t.Errorf("a known folder wrote again: %v", got)
	}
}

// A record read at the start counts as written: a spawn in its folder does not
// write it again.
func TestRecordReadAtTheStartIsNotWrittenAgain(t *testing.T) {
	dir := recordDir(t)
	s := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, RecordName(s)), fmt.Appendf(nil, "1000 1 2\n%s", s), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseForTest(dir))
	if err := Remember(s); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, RecordName(s))); err != nil || !strings.HasPrefix(string(b), "1000 1 2\n") {
		t.Errorf("the record read at the start holds %q (%v), want it unchanged", b, err)
	}
}

// The records of an earlier daemon count: a new store reads them at its start, and
// its git start passes over a git in such a folder with no spawn.
func TestRecordsCountAfterARestart(t *testing.T) {
	dir := recordDir(t)
	s := t.TempDir()
	bin := binWithGit(t, s)
	_ = Remember(s)
	t.Cleanup(UseForTest(dir))
	other := binWithGit(t, t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+other)
	cmd := exec.Command("git")
	PickGit(cmd)
	if want := filepath.Join(other, gitFile()); cmd.Err != nil || cmd.Path != want {
		t.Fatalf("after the restart git is %q (%v), want %q", cmd.Path, cmd.Err, want)
	}
}

// A folder at the name of the record fails the write with "file exists". The
// folder and its file stay, the temp file goes, and the line of the failure
// names the record folder.
func TestFolderAtTheRecordNameFailsTheWrite(t *testing.T) {
	dir := recordDir(t)
	buf := captureLog(t)
	s := t.TempDir()
	planted := filepath.Join(dir, RecordName(s))
	if err := os.Mkdir(planted, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planted, "planted.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A failed rename refuses no spawn.
	if err := Remember(s); err != nil {
		t.Errorf("Remember after a failed rename = %v, want nil", err)
	}
	if got := names(t, dir); !slices.Equal(got, []string{RecordName(s)}) {
		t.Errorf("the record folder holds %v, want the planted folder only", got)
	}
	if _, err := os.Stat(filepath.Join(planted, "planted.txt")); err != nil {
		t.Errorf("the planted file: %v", err)
	}
	want := regexp.MustCompile(`\[sessiondirs\] cannot write to the record of session folders in ` +
		regexp.QuoteMeta(fmt.Sprintf("%q", filepath.ToSlash(dir))) +
		`: renameat \.new-` + fmt.Sprint(os.Getpid()) + `-[0-9]+ ` + RecordName(s) +
		`: file exists; they are kept in memory until this helper ends, and written at a later start\n`)
	if !want.MatchString(buf.String()) {
		t.Errorf("log = %q, want the line of the failed write", buf.String())
	}
	// The folder is still known in memory.
	bin := binWithGit(t, s)
	t.Setenv("PATH", bin)
	cmd := exec.Command("git")
	PickGit(cmd)
	if !errors.As(cmd.Err, new(NoOtherGitError)) {
		t.Errorf("after the failed write git is %q (%v), want the refusal", cmd.Path, cmd.Err)
	}
}

// A file at the path of the record folder fails the record before the rename.
// With a PATH entry in the folder, first or empty, Remember returns SaveError, so
// the spawn starts no child. With no such entry it returns nil. Both keep the
// folder in memory and log the line of the failure.
func TestUnsavedRecordRefusesWithAPathEntryInTheFolder(t *testing.T) {
	if want := "Claude couldn't save its list of session folders to ~/.claude/remote on this host. Free up disk space or fix that folder's permissions"; (SaveError{}).Error() != want {
		t.Errorf("SaveError = %q, want %q", SaveError{}.Error(), want)
	}
	other := t.TempDir()
	for _, c := range []struct {
		name   string
		path   func(s string) string
		refuse bool
	}{
		{"git in the first entry", func(s string) string { return binWithGit(t, s) + string(os.PathListSeparator) + other }, true},
		{"empty first entry", func(s string) string {
			bin := filepath.Join(s, "bin")
			if err := os.Mkdir(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			return bin + string(os.PathListSeparator) + other
		}, true},
		{"git not on PATH", func(s string) string { binWithGit(t, s); return other }, false},
		{"empty folder not on PATH", func(string) string { return other }, false},
	} {
		dir := filepath.Join(t.TempDir(), RecordFolderName)
		if err := os.WriteFile(dir, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		restore := UseForTest(dir)
		buf := captureLog(t)
		s := t.TempDir()
		t.Setenv("PATH", c.path(s))
		err := Remember(s)
		if got := errors.As(err, new(SaveError)); got != c.refuse || !c.refuse && err != nil {
			t.Errorf("%s: Remember = %v, want SaveError %v", c.name, err, c.refuse)
		}
		if !strings.Contains(buf.String(), "[sessiondirs] cannot write to the record of session folders in "+fmt.Sprintf("%q", filepath.ToSlash(dir))+": ") {
			t.Errorf("%s: log = %q, want the line of the failed write", c.name, buf.String())
		}
		cur := current.Load()
		if !cur.known[s] {
			t.Errorf("%s: the folder is not kept in memory", c.name)
		}
		restore()
	}
}

// A failed save does not count as a record: the next Remember of the folder tries
// the write again and fails again. After the file at the record folder goes, the
// write makes the folder with mode 0700 and writes the record.
func TestUnsavedRecordIsTriedAgain(t *testing.T) {
	dir := filepath.Join(t.TempDir(), RecordFolderName)
	if err := os.WriteFile(dir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseForTest(dir))
	s := t.TempDir()
	t.Setenv("PATH", binWithGit(t, s))
	for i := range 2 {
		if err := Remember(s); !errors.As(err, new(SaveError)) {
			t.Fatalf("Remember %d with a file at the record folder = %v, want SaveError", i+1, err)
		}
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := Remember(s); err != nil {
		t.Fatalf("Remember after the file went = %v", err)
	}
	if got := names(t, dir); !slices.Equal(got, []string{RecordName(s)}) {
		t.Errorf("after the file went the record folder holds %v, want the record of %s", got, s)
	}
	if fi, err := os.Stat(dir); runtime.GOOS != "windows" && (err != nil || fi.Mode().Perm() != 0o700) {
		t.Errorf("the record folder made again: %v %v, want mode 0700", fi, err)
	}
}

// The record of a create runs no prune: with the folder at the cap, it makes one
// record more and removes none. A spawn record after it prunes.
func TestCreateRecordRunsNoPrune(t *testing.T) {
	dir := recordDir(t)
	old := recordCap
	recordCap = 1
	t.Cleanup(func() { recordCap = old })
	first := t.TempDir()
	if err := Remember(first); err != nil {
		t.Fatal(err)
	}
	if err := RememberCreate(filepath.Join(t.TempDir(), ".git")); err != nil {
		t.Fatal(err)
	}
	if got := names(t, dir); len(got) != 2 {
		t.Errorf("after the create record the folder holds %v, want two records", got)
	}
	if err := Remember(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if got := names(t, dir); len(got) != 1 {
		t.Errorf("after the spawn record the folder holds %v, want one record", got)
	}
}

// Above the cap the records with the lowest ms field go, each by its name, and
// the file time does not count. A record whose folder is gone stays when it is not
// among the lowest. Other files stay.
func TestPruneRemovesTheLowestMsRecords(t *testing.T) {
	dir := recordDir(t)
	old := recordCap
	recordCap = 3
	t.Cleanup(func() { recordCap = old })
	if err := os.WriteFile(filepath.Join(dir, "keep-me"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	// Three planted records: the ms field rises with i, and the file time falls.
	var planted []string
	now := time.Now()
	for i := range 3 {
		f := filepath.Join(base, fmt.Sprint(i))
		name := filepath.Join(dir, RecordName(f))
		if err := os.WriteFile(name, fmt.Appendf(nil, "%d 1 2\n%s", 1000+i, f), 0o600); err != nil {
			t.Fatal(err)
		}
		at := now.Add(-time.Duration(3-i) * time.Hour)
		if i == 0 {
			at = now.Add(-time.Minute)
		}
		if err := os.Chtimes(name, at, at); err != nil {
			t.Fatal(err)
		}
		planted = append(planted, f)
	}
	t.Cleanup(UseForTest(dir))
	n1 := filepath.Join(base, "n1")
	_ = Remember(n1)
	want := []string{"keep-me", RecordName(planted[1]), RecordName(planted[2]), RecordName(n1)}
	slices.Sort(want)
	if got := names(t, dir); !slices.Equal(got, want) {
		t.Errorf("after the prune the folder holds %v, want %v", got, want)
	}
}

// PickGit passes over a git in a session folder, also through a PATH entry that
// is reached through one, and logs the path once. With no other git the start
// fails with the refusal. A request with the off switch keeps the first git.
func TestPickGitPassesOverASessionGit(t *testing.T) {
	t.Cleanup(UseForTest(""))
	buf := captureLog(t)
	s := t.TempDir()
	bin := binWithGit(t, s)
	other := binWithGit(t, t.TempDir())
	_ = Remember(s)
	sep := string(os.PathListSeparator)
	t.Setenv("PATH", bin+sep+other)
	cmd := exec.Command("git")
	PickGit(cmd)
	if want := filepath.Join(other, gitFile()); cmd.Err != nil || cmd.Path != want {
		t.Fatalf("git is %q (%v), want %q", cmd.Path, cmd.Err, want)
	}
	line := fmt.Sprintf(`[sessiondirs] passing over %q: it lies in, or is reached through, a folder that Claude works in. Each file in "" ends in the path of one such folder; remove the file to have that folder forgotten`+"\n",
		filepath.ToSlash(filepath.Join(bin, gitFile())))
	if n := strings.Count(buf.String(), line); n != 1 {
		t.Errorf("log = %q, want the passing-over line once", buf.String())
	}
	PickGit(exec.Command("git"))
	if n := strings.Count(buf.String(), line); n != 1 {
		t.Errorf("the second start logged the line again: %q", buf.String())
	}

	t.Setenv("PATH", bin)
	cmd = exec.Command("git")
	PickGit(cmd)
	if !errors.As(cmd.Err, new(NoOtherGitError)) {
		t.Fatalf("with no other git: %q %v, want the refusal", cmd.Path, cmd.Err)
	}
	if err := cmd.Run(); !errors.As(err, new(NoOtherGitError)) {
		t.Errorf("the start = %v, want the refusal", err)
	}

	release := PauseCheck()
	cmd = exec.Command("git")
	PickGit(cmd)
	release()
	release()
	// A second request with the switch logs no second OFF line, and the on-again
	// line comes at the next request without the switch.
	PauseCheck()()
	if strings.Contains(buf.String(), "is on again") {
		t.Errorf("the on-again line came at the release: %q", buf.String())
	}
	on()
	on()
	if want := filepath.Join(bin, gitFile()); cmd.Err != nil || cmd.Path != want {
		t.Errorf("with the off switch git is %q (%v), want %q", cmd.Path, cmd.Err, want)
	}
	for _, l := range []string{
		"[sessiondirs] the check of session folders is OFF: the app sends its off switch. While a request that carries it runs, git is found by its bare name and its search lists are cut by text alone\n",
		"[sessiondirs] the check of session folders is on again: no request that carries the app's off switch holds it any more\n",
	} {
		if n := strings.Count(buf.String(), l); n != 1 {
			t.Errorf("the line %q came %d times, want once", l, n)
		}
	}
	cmd = exec.Command("git")
	PickGit(cmd)
	if !errors.As(cmd.Err, new(NoOtherGitError)) {
		t.Errorf("after the release: %q %v, want the refusal", cmd.Path, cmd.Err)
	}
}

// CutPath takes the session entries out of the list variables of a git child,
// and a list with none left goes. A program variable in a session folder goes.
// Other names stay as given, and so does the whole env while the check is off.
func TestCutPathTakesOutSessionEntries(t *testing.T) {
	t.Cleanup(UseForTest(""))
	s := t.TempDir()
	_ = Remember(s)
	sep := string(os.PathListSeparator)
	other := t.TempDir()
	in := filepath.Join(s, "x")
	env := []string{
		"A=1",
		"PATH=" + filepath.Join(s, "bin") + sep + other + sep + s,
		"GIT_EXEC_PATH=" + in + sep + other,
		"LD_LIBRARY_PATH=" + in,
		"DYLD_FRAMEWORK_PATH=" + in + sep + other,
		"GIT_SSH=" + in,
		"GIT_ASKPASS=" + filepath.Join(other, "askpass"),
		"SSH_ASKPASS=" + in,
		"PYTHONPATH=" + in,
		"PERL5LIB=" + in,
		"MANPATH=" + in,
		"B=" + s,
	}
	got := CutPath(env)
	want := []string{
		"A=1",
		"PATH=" + other,
		"GIT_EXEC_PATH=" + other,
		"DYLD_FRAMEWORK_PATH=" + other,
		"GIT_ASKPASS=" + filepath.Join(other, "askpass"),
		"PYTHONPATH=" + in,
		"PERL5LIB=" + in,
		"MANPATH=" + in,
		"B=" + s,
	}
	if !slices.Equal(got, want) {
		t.Errorf("CutPath = %q\nwant %q", got, want)
	}
	if env[1] == got[1] {
		t.Errorf("CutPath changed the env of the caller")
	}
	plain := []string{"PATH=" + other}
	if got := CutPath(plain); !slices.Equal(got, plain) {
		t.Errorf("CutPath with nothing to cut = %q", got)
	}
	release := PauseCheck()
	defer release()
	if got := CutPath(env); !slices.Equal(got, env) {
		t.Errorf("CutPath while off = %q, want the env as given", got)
	}
	if got := TraceOff(env); !slices.Equal(got, env) {
		t.Errorf("TraceOff while off = %q, want the env as given", got)
	}
}

// A spawn without the switch records its folder also while a request with the
// switch holds the check off.
func TestRememberRecordsDuringTheHold(t *testing.T) {
	dir := recordDir(t)
	captureLog(t)
	release := PauseCheck()
	f := t.TempDir()
	_ = Remember(f)
	release()
	if got := names(t, dir); len(got) != 1 || got[0] != RecordName(f) {
		t.Errorf("a spawn during the hold wrote %v, want its record", got)
	}
}

// The record folder of a socket <root>/run/<client>/rpc.sock is
// <root>/session-folders. Another socket keeps the folders in memory only.
func TestRecordDirFor(t *testing.T) {
	root := t.TempDir()
	for _, c := range []struct {
		sock string
		ok   bool
	}{
		{filepath.Join(root, "run", "c1", "rpc.sock"), true},
		{filepath.Join(root, "run", "0123abcd", "rpc.sock"), true},
		{filepath.Join(root, "x", "c1", "rpc.sock"), false},
		{filepath.Join(root, "run", "c1", "s.sock"), false},
	} {
		dir, ok := RecordDirFor(c.sock)
		if ok != c.ok || ok && dir != filepath.Join(root, RecordFolderName) {
			t.Errorf("RecordDirFor(%q) = %q %v", c.sock, dir, ok)
		}
	}
}

// Open makes the record folder with mode 0700 and reads its records. A socket of
// another layout logs the line and writes no record.
func TestOpenMakesTheFolderOrKeepsMemory(t *testing.T) {
	t.Cleanup(UseForTest(""))
	buf := captureLog(t)
	root := t.TempDir()
	Open(filepath.Join(root, "run", "c1", "rpc.sock"))
	dir := filepath.Join(root, RecordFolderName)
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() {
		t.Fatalf("the record folder: %v", err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700 {
		t.Errorf("the record folder mode is %v, want 0700", fi.Mode().Perm())
	}
	s := t.TempDir()
	_ = Remember(s)
	if _, err := os.Stat(filepath.Join(dir, RecordName(s))); err != nil {
		t.Errorf("the record: %v", err)
	}

	sock := filepath.Join(root, "d", "rpc.sock")
	Open(sock)
	line := fmt.Sprintf("[daemon] socket %q is not <root>/run/<client>/rpc.sock: session folders are kept in memory only, so the next helper will not know them\n", filepath.ToSlash(sock))
	if !strings.Contains(buf.String(), line) {
		t.Errorf("log = %q, want %q", buf.String(), line)
	}
	s2 := t.TempDir()
	_ = Remember(s2)
	if _, err := os.Stat(filepath.Join(dir, RecordName(s2))); !os.IsNotExist(err) {
		t.Errorf("a store in memory wrote a record: %v", err)
	}
	if !current.Load().holds(filepath.Join(s2, "bin", "git")) {
		t.Errorf("the store in memory does not hold %s", s2)
	}
}

// TraceOff sets the three trace names of git to 0 at the end of env, in place of
// any earlier entries of those names.
func TestTraceOffSetsTheTraceNames(t *testing.T) {
	t.Cleanup(UseForTest(""))
	got := TraceOff([]string{"A=1", "GIT_TRACE2=/tmp/x", "B=2"})
	want := []string{"A=1", "B=2", "GIT_TRACE2=0", "GIT_TRACE2_EVENT=0", "GIT_TRACE2_PERF=0"}
	if !slices.Equal(got, want) {
		t.Errorf("TraceOff = %q, want %q", got, want)
	}
}

// A request that runs during the hold gets no check. After the hold its first
// check logs the on-again line, and then its passing-over line.
func TestOnAgainLineComesAtTheFirstCheckAfterTheHold(t *testing.T) {
	t.Cleanup(UseForTest(""))
	buf := captureLog(t)
	s := t.TempDir()
	bin := binWithGit(t, s)
	other := binWithGit(t, t.TempDir())
	_ = Remember(s)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+other)
	release := PauseCheck()
	cmd := exec.Command("git")
	PickGit(cmd)
	if want := filepath.Join(bin, gitFile()); cmd.Path != want {
		t.Errorf("during the hold git is %q, want %q", cmd.Path, want)
	}
	release()
	if strings.Contains(buf.String(), "on again") {
		t.Fatalf("the on-again line came at the release: %q", buf.String())
	}
	PickGit(exec.Command("git"))
	log := buf.String()
	on, pass := strings.Index(log, "is on again"), strings.Index(log, "passing over")
	if on < 0 || pass < 0 || on > pass {
		t.Errorf("log = %q, want the on-again line before the passing-over line", log)
	}
}
