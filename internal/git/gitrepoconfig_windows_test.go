//go:build windows

package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Windows rules of the config read. 5fd08069 answered these texts on
// Windows (measured).

// TestWindowsIncludeTexts pins the two include texts with the paths of the
// cells. The values `~`, `/x`, `\x` and `C:x` are claustrum's own, not measured. The expected texts are the message of the reply, with the JSON
// escapes taken off.
func TestWindowsIncludeTexts(t *testing.T) {
	const config = `C:\o\w\repo\.git\config`
	notRelative := func(value string) string {
		return `"C:\\o\\w\\repo\\.git\\config" includes ` + strconv.Quote(value) +
			`, and on Windows Claude follows an include only if its path is relative and stays inside the repository; ` +
			`if that line is yours and you trust the file, put the line into ~/.gitconfig on the remote machine instead`
	}
	for _, value := range []string{"C:/o/w/absinc.cfg", "~/x", "C:/o/w/missing.cfg", "~", "/x", `\x`, "C:x"} {
		if got, want := includeValueDetail(config, value), notRelative(value); got != want {
			t.Errorf("include %s: detail = %q, want %q", value, got, want)
		}
	}
	for _, value := range []string{"inc.cfg", "../../outinc.cfg"} {
		if got := includeValueDetail(config, value); got != "" {
			t.Errorf("relative include %s: detail = %q, want none", value, got)
		}
	}
	// A value of 400 bytes is cut to its first 300 characters (measured).
	// Measurements have the pair 300 and 301.
	long := "~root/" + strings.Repeat("x", 394)
	if got, want := includeValueDetail(config, long), notRelative(long[:300]); got != want {
		t.Errorf("include value of 400 bytes: detail = %q, want %q", got, want)
	}

	const outside = `"C:\\o\\w\\outinc.cfg" is outside "C:\\o\\w\\repo", and on Windows Claude reads a repository's ` +
		`settings only from inside it; if the line that includes it is yours and you trust the file, ` +
		`put that line into ~/.gitconfig on the remote machine instead`
	if got := includePlaceDetail(`C:\o\w\outinc.cfg`, `C:\o\w\repo\.git`); got != outside {
		t.Errorf("include outside the repository: detail = %q, want %q", got, outside)
	}
	for _, inside := range []string{`C:\o\w\repo\.git\inc.cfg`, `C:\o\w\repo\x.cfg`} {
		if got := includePlaceDetail(inside, `C:\o\w\repo\.git`); got != "" {
			t.Errorf("include %s: detail = %q, want none", inside, got)
		}
	}
}

// TestWindowsIncludeNotLocal pins the "is outside" text of an include below the
// repository that filepath.IsLocal does not accept. `a/inc.cfg::$DATA` and `NUL`
// are b3081b51, measured on Windows. `a:b` and `COM1` are claustrum's own, not
// measured. The other six forms pass on b3081b51 (measured on Windows).
func TestWindowsIncludeNotLocal(t *testing.T) {
	const gitDir = `C:\o\w\repo\.git`
	outside := func(cleaned string) string {
		return strconv.Quote(cleaned) + ` is outside "C:\\o\\w\\repo", and on Windows Claude reads a repository's ` +
			`settings only from inside it; if the line that includes it is yours and you trust the file, ` +
			`put that line into ~/.gitconfig on the remote machine instead`
	}
	for _, c := range []struct{ value, cleaned string }{
		{"a/inc.cfg::$DATA", gitDir + `\a\inc.cfg::$DATA`},
		{"NUL", gitDir + `\NUL`},
		{"a:b", gitDir + `\a:b`},
		{"a/COM1", gitDir + `\a\COM1`},
	} {
		if got, want := includePlaceDetail(gitDir+`\`+c.value, gitDir), outside(c.cleaned); got != want {
			t.Errorf("include %s: detail = %q, want %q", c.value, got, want)
		}
	}
	for _, value := range []string{"a/inc.cfg", "a./inc.cfg", "a /inc.cfg", "a/inc.cfg.", "a/inc.cfg ", "b/../a/inc.cfg"} {
		if got := includePlaceDetail(gitDir+`\`+value, gitDir); got != "" {
			t.Errorf("include %s: detail = %q, want none", value, got)
		}
	}
}

// TestWindowsIncludeThroughTheRead runs the two include rules through the read
// of a real repository.
func TestWindowsIncludeThroughTheRead(t *testing.T) {
	repo, gitDir := configRepo(t)
	config := filepath.Join(gitDir, "config")
	outside := filepath.Join(filepath.Dir(repo), "outinc.cfg")
	if err := os.WriteFile(outside, []byte(refusedInclude), 0o644); err != nil {
		t.Fatal(err)
	}

	setRepoConfig(t, gitDir, version0+"[include]\n\tpath = ../../outinc.cfg\n")
	want := strconv.Quote(outside) + " is outside " + strconv.Quote(repo) +
		", and on Windows Claude reads a repository's settings only from inside it; " +
		"if the line that includes it is yours and you trust the file, put that line into ~/.gitconfig on the remote machine instead"
	if got := configDetail(t, gitDir); got != want {
		t.Errorf("relative include that leaves the repository: detail = %q, want %q", got, want)
	}

	value := filepath.ToSlash(outside)
	setRepoConfig(t, gitDir, version0+"[include]\n\tpath = "+value+"\n")
	want = strconv.Quote(config) + " includes " + strconv.Quote(value) +
		", and on Windows Claude follows an include only if its path is relative and stays inside the repository; " +
		"if that line is yours and you trust the file, put the line into ~/.gitconfig on the remote machine instead"
	if got := configDetail(t, gitDir); got != want {
		t.Errorf("absolute include: detail = %q, want %q", got, want)
	}

	// `NUL` and a stream name get the "is outside" text with the cleaned path, not
	// the "not a regular file" text (b3081b51, measured on Windows).
	setIncludeFile(t, gitDir, "inc.cfg", refusedInclude)
	for _, c := range []struct{ value, cleaned string }{
		{"NUL", filepath.Join(gitDir, "NUL")},
		{"inc.cfg::$DATA", filepath.Join(gitDir, "inc.cfg::$DATA")},
	} {
		setRepoConfig(t, gitDir, version0+"[include]\n\tpath = "+c.value+"\n")
		want = strconv.Quote(c.cleaned) + " is outside " + strconv.Quote(repo) +
			", and on Windows Claude reads a repository's settings only from inside it; " +
			"if the line that includes it is yours and you trust the file, put that line into ~/.gitconfig on the remote machine instead"
		if got := configDetail(t, gitDir); got != want {
			t.Errorf("include %s: detail = %q, want %q", c.value, got, want)
		}
	}

	// A relative include inside the git directory is not refused (measured).
	// claustrum follows it.
	setIncludeFile(t, gitDir, "inc.cfg", markerInclude)
	setRepoConfig(t, gitDir, version0+"[include]\n\tpath = inc.cfg\n")
	if got, want := configDetail(t, gitDir), markerDetail(filepath.Join(gitDir, "inc.cfg")); got != want {
		t.Errorf("relative include inside the git directory: detail = %q, want %q", got, want)
	}

	// The path of an included file is the folder of its file, one backslash and
	// the value as written: the slash of `sub/inc.cfg` stays (measured).
	if err := os.Mkdir(filepath.Join(gitDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	setIncludeFile(t, gitDir, filepath.Join("sub", "inc.cfg"), "[include]\n\tpath = ~root/x\n")
	setRepoConfig(t, gitDir, version0+"[include]\n\tpath = sub/inc.cfg\n")
	want = strconv.Quote(gitDir+`\sub/inc.cfg`) + ` includes "~root/x"` +
		", and on Windows Claude follows an include only if its path is relative and stays inside the repository; " +
		"if that line is yours and you trust the file, put the line into ~/.gitconfig on the remote machine instead"
	if got := configDetail(t, gitDir); got != want {
		t.Errorf("include line in a file reached by sub/inc.cfg: detail = %q, want %q", got, want)
	}
}

// TestWindowsIncludeThroughAJunction pins the link text of an include whose folder
// is a junction, with the path as written (b3081b51, measured on Windows). The
// directory symbolic link is claustrum's own, not measured. The same file by its
// real folder is followed.
func TestWindowsIncludeThroughAJunction(t *testing.T) {
	_, gitDir := configRepo(t)
	realDir := filepath.Join(gitDir, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	setIncludeFile(t, gitDir, filepath.Join("real", "inc.cfg"), markerInclude)
	setRepoConfig(t, gitDir, version0+"[include]\n\tpath = real/inc.cfg\n")
	if got, want := configDetail(t, gitDir), markerDetail(gitDir+`\real/inc.cfg`); got != want {
		t.Errorf("include by the real folder: detail = %q, want %q", got, want)
	}
	through := func(name string) {
		t.Helper()
		setRepoConfig(t, gitDir, version0+"[include]\n\tpath = "+name+"/inc.cfg\n")
		want := strconv.Quote(gitDir+`\`+name+`/inc.cfg`) + ": is, or is reached through, a link that Claude does not follow here; " +
			"replace the link with what it points to"
		if got := configDetail(t, gitDir); got != want {
			t.Errorf("include through %s: detail = %q, want %q", name, got, want)
		}
	}
	makeJunction(t, filepath.Join(gitDir, "jn"), realDir)
	through("jn")
	// makeDirSymlink skips the rest of the test on a host that makes no such link.
	makeDirSymlink(t, filepath.Join(gitDir, "sl"), realDir)
	through("sl")
}

// TestWindowsConfigLink pins the refusal of a config that is a file link
// (measured). A host that refuses to make a symbolic link skips the test.
func TestWindowsConfigLink(t *testing.T) {
	repo, gitDir := configRepo(t)
	config := filepath.Join(gitDir, "config")
	realFile := filepath.Join(filepath.Dir(repo), "realconfig")
	if err := os.Rename(config, realFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realFile, config); err != nil {
		t.Skipf("this host makes no symbolic link: %v", err)
	}
	want := strconv.Quote(config) + ": is, or is reached through, a link that Claude does not follow here; " +
		"replace the link with what it points to"
	if got := configDetail(t, gitDir); got != want {
		t.Errorf("config that is a file link: detail = %q, want %q", got, want)
	}
}

// pipeCounter is a named pipe server. It makes the next instance before it
// handles a connected client. Between a connect and that next instance, a
// client finds all instances busy, so a marker client opens with
// openMarker. It handles clients one at a time, in the order that
// they connect. It counts each client that writes no byte, also one that left
// before the server took it. A marker client writes one byte and reads one byte
// back.
type pipeCounter struct {
	name     string
	waiting  atomic.Int64
	connects atomic.Int64
	stop     atomic.Bool
	done     chan struct{}
}

var procPeekNamedPipe = windows.NewLazySystemDLL("kernel32.dll").NewProc("PeekNamedPipe")

func startPipeCounter(t *testing.T, name string) *pipeCounter {
	t.Helper()
	name16, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	p := &pipeCounter{name: name, done: make(chan struct{})}
	errs := make(chan error, 1)
	create := func() (windows.Handle, error) {
		h, err := windows.CreateNamedPipe(name16, windows.PIPE_ACCESS_DUPLEX,
			windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT,
			windows.PIPE_UNLIMITED_INSTANCES, 512, 512, 0, nil)
		if err == nil {
			p.waiting.Add(1)
		}
		return h, err
	}
	cur, err := create()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(p.done)
		for {
			err := windows.ConnectNamedPipe(cur, nil)
			if p.stop.Load() {
				_ = windows.CloseHandle(cur)
				return
			}
			// The next instance waits before this one leaves the wait.
			next, cerr := create()
			if cerr != nil {
				errs <- cerr
				_ = windows.CloseHandle(cur)
				return
			}
			p.waiting.Add(-1)
			switch {
			case err == nil || errors.Is(err, windows.ERROR_PIPE_CONNECTED):
				p.take(cur)
			case errors.Is(err, windows.ERROR_NO_DATA):
				p.connects.Add(1)
			default:
				errs <- err
				_ = windows.CloseHandle(cur)
				_ = windows.CloseHandle(next)
				return
			}
			_ = windows.DisconnectNamedPipe(cur)
			_ = windows.CloseHandle(cur)
			cur = next
		}
	}()
	t.Cleanup(func() {
		p.stop.Store(true)
		// This client wakes the instance that waits. A client in take ends in 5 s.
		if f, err := os.OpenFile(name, os.O_RDWR, 0); err == nil {
			_ = f.Close()
		}
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
			t.Error("the pipe server did not stop in 10 s")
		}
		select {
		case err := <-errs:
			t.Errorf("pipe server: %v", err)
		default:
		}
	})
	return p
}

// take handles one connected client. It waits up to 5 s for one byte, so a
// client that reads the pipe gets an end. A client that leaves or writes
// nothing counts as a connect.
func (p *pipeCounter) take(h windows.Handle) {
	end := time.Now().Add(5 * time.Second)
	for time.Now().Before(end) {
		var avail uint32
		r, _, _ := procPeekNamedPipe.Call(uintptr(h), 0, 0, 0, uintptr(unsafe.Pointer(&avail)), 0)
		if r == 0 {
			break
		}
		if avail > 0 {
			var n uint32
			b := make([]byte, 1)
			if windows.ReadFile(h, b, &n, nil) == nil && n == 1 {
				_ = windows.WriteFile(h, b, &n, nil)
				_ = windows.FlushFileBuffers(h)
				return
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	p.connects.Add(1)
}

// listening waits until the server has an instance that exists and waits for
// a client. Only the test connects, so the instance waits until the next step.
func (p *pipeCounter) listening(t *testing.T) {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for p.waiting.Load() < 1 {
		if time.Now().After(end) {
			t.Fatal("the pipe server has no waiting instance in 10 s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// openMarker opens the pipe for a marker client. A busy pipe or one with no
// instance makes no connect, so it tries again for up to 5 s.
func (p *pipeCounter) openMarker() (*os.File, error) {
	end := time.Now().Add(5 * time.Second)
	for {
		f, err := os.OpenFile(p.name, os.O_RDWR, 0)
		gap := errors.Is(err, windows.ERROR_PIPE_BUSY) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND)
		if !gap || time.Now().After(end) {
			return f, err
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// count connects a marker client and returns the connects before it. The
// server takes clients in order, so the answer to the marker ends the count.
func (p *pipeCounter) count(t *testing.T) int64 {
	t.Helper()
	p.listening(t)
	answered := make(chan error, 1)
	go func() {
		f, err := p.openMarker()
		if err != nil {
			answered <- err
			return
		}
		defer func() { _ = f.Close() }()
		if _, err := f.Write([]byte{1}); err != nil {
			answered <- err
			return
		}
		_, err = f.Read(make([]byte, 1))
		answered <- err
	}()
	select {
	case err := <-answered:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the pipe server answered no marker in 10 s")
	}
	return p.connects.Load()
}

// TestWindowsConfigLinkToAPipe pins that two reads of a config that is a link to
// a named pipe make no connect to the pipe. b3081b51 makes none, and the replies
// are equal (measured on Windows). Before each step the server has an instance
// that waits, so a connect of the step reaches it, and a marker connect ends each
// count. The change test of a state of the regular config before the swap makes
// no connect either. One Stat of the link is the control: it counts one connect,
// so the count works. A host that refuses to make a symbolic link skips the test.
func TestWindowsConfigLinkToAPipe(t *testing.T) {
	_, gitDir := configRepo(t)
	config := filepath.Join(gitDir, "config")
	pipe := fmt.Sprintf(`\\.\pipe\claustrum-config-link-%d-%d`, os.Getpid(), time.Now().UnixNano())
	p := startPipeCounter(t, pipe)
	before := readRepoFileState(config, true)
	if err := os.Remove(config); err != nil {
		t.Fatal(err)
	}
	// mklink makes a file link and opens nothing at its target.
	if out, err := exec.Command("cmd", "/c", "mklink", config, pipe).CombinedOutput(); err != nil {
		t.Skipf("this host makes no symbolic link: mklink %s %s: %v\n%s", config, pipe, err, out)
	}
	if n := p.count(t); n != 0 {
		t.Fatalf("connects to the pipe before the reads = %d, want 0", n)
	}
	p.listening(t)
	if before.unchanged() {
		t.Error("change test of the regular config after the swap to a link: unchanged, want changed")
	}
	if n := p.count(t); n != 0 {
		t.Errorf("connects to the pipe after the change test of the regular config = %d, want 0", n)
	}
	want := strconv.Quote(config) + ": is, or is reached through, a link that Claude does not follow here; " +
		"replace the link with what it points to"
	for i := range 2 {
		p.listening(t)
		if got := configDetail(t, gitDir); got != want {
			t.Errorf("read %d of a config link to a pipe: detail = %q, want %q", i+1, got, want)
		}
	}
	if n := p.count(t); n != 0 {
		t.Errorf("connects to the pipe after two reads = %d, want 0", n)
	}
	p.listening(t)
	_, _ = os.Stat(config)
	if n := p.count(t); n != 1 {
		t.Errorf("connects to the pipe after one Stat of the link = %d, want 1", n)
	}
}

// shallowLinkDetail is the link refusal of the file `shallow` of gitDir.
func shallowLinkDetail(gitDir string) string {
	return strconv.Quote(filepath.Join(gitDir, "shallow")) + ": is, or is reached through, a link that Claude does not follow here; " +
		"replace the link with what it points to"
}

// TestWindowsShallowLinkToAPipe pins that a `shallow` that is a link to a named
// pipe gets the link refusal and makes no connect to the pipe. b3081b51 answers
// so and makes 0 connects (measured on Windows). One Stat of the link is the
// control: it counts one connect, so the count works. A host that refuses to make
// a symbolic link skips the test.
func TestWindowsShallowLinkToAPipe(t *testing.T) {
	_, gitDir := configRepo(t)
	shallow := filepath.Join(gitDir, "shallow")
	pipe := fmt.Sprintf(`\\.\pipe\claustrum-shallow-link-%d-%d`, os.Getpid(), time.Now().UnixNano())
	p := startPipeCounter(t, pipe)
	// mklink makes a file link and opens nothing at its target.
	if out, err := exec.Command("cmd", "/c", "mklink", shallow, pipe).CombinedOutput(); err != nil {
		t.Skipf("this host makes no symbolic link: mklink %s %s: %v\n%s", shallow, pipe, err, out)
	}
	if n := p.count(t); n != 0 {
		t.Fatalf("connects to the pipe before the tests = %d, want 0", n)
	}
	for i := range 2 {
		p.listening(t)
		if got, want := gitDirFilesDetail(gitDir, nil), shallowLinkDetail(gitDir); got != want {
			t.Errorf("test %d of a shallow link to a pipe: detail = %q, want %q", i+1, got, want)
		}
	}
	if n := p.count(t); n != 0 {
		t.Errorf("connects to the pipe after two tests = %d, want 0", n)
	}
	p.listening(t)
	_, _ = os.Stat(shallow)
	if n := p.count(t); n != 1 {
		t.Errorf("connects to the pipe after one Stat of the link = %d, want 1", n)
	}
}

// TestWindowsShallowLinkAndFile pins that a `shallow` that is a link to NUL gets
// the link refusal, and a regular `shallow` with one object id passes. b3081b51
// answers so (measured on Windows). A host that refuses to make a symbolic link
// skips the link half.
func TestWindowsShallowLinkAndFile(t *testing.T) {
	_, gitDir := configRepo(t)
	shallow := filepath.Join(gitDir, "shallow")
	writeFile(t, shallow, strings.Repeat("a", 40)+"\n", 0o644)
	if got := gitDirFilesDetail(gitDir, nil); got != "" {
		t.Errorf("a regular shallow: detail = %q, want none", got)
	}
	if err := os.Remove(shallow); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("NUL", shallow); err != nil {
		t.Skipf("this host makes no symbolic link: %v", err)
	}
	if got, want := gitDirFilesDetail(gitDir, nil), shallowLinkDetail(gitDir); got != want {
		t.Errorf("a shallow link to NUL: detail = %q, want %q", got, want)
	}
}
