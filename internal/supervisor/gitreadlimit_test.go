package supervisor

import (
	"bufio"
	"net"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/conn"
)

// rawLineConn returns a *conn.Conn whose written lines come out of the channel as
// they went on the wire.
func rawLineConn(t *testing.T) (*conn.Conn, <-chan string) {
	t.Helper()
	client, server := net.Pipe()
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(server)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	t.Cleanup(func() { client.Close(); server.Close() })
	return conn.New(client, nil, 0, nil, nil, nil), lines
}

// nextLine waits up to d for the next line that holds part.
func nextLine(t *testing.T, lines <-chan string, part string, d time.Duration) string {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case l := <-lines:
			if strings.Contains(l, part) {
				return l
			}
		case <-deadline:
			t.Fatalf("no line with %s within %v", part, d)
			return ""
		}
	}
}

// withoutEndAt puts <n> for the "at" value of the end member of a frame line. The
// value is the byte offset on the connection, and the rest of the line is exact.
func withoutEndAt(line string) string {
	return regexp.MustCompile(`"end":\{"at":[0-9]+,`).ReplaceAllString(line, `"end":{"at":<n>,`)
}

// recordSignalsByProc records the names of the signals that go through the signalGroup
// seam, by process, until the test ends. Every signal still goes on to its target.
// It is set before the spawn, so the goroutine of the limit reads it.
func recordSignalsByProc(t *testing.T) func(proc *os.Process) []string {
	t.Helper()
	realSignal := signalGroup
	var mu sync.Mutex
	sent := map[*os.Process][]string{}
	signalGroup = func(g *ProcGroup, proc *os.Process, signame string) error {
		mu.Lock()
		sent[proc] = append(sent[proc], signame)
		mu.Unlock()
		return realSignal(g, proc, signame)
	}
	t.Cleanup(func() { signalGroup = realSignal })
	return func(proc *os.Process) []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(sent[proc])
	}
}

// setGitReadLimitForTest shortens the limit and the wait after the SIGTERM until
// the test ends.
func setGitReadLimitForTest(t *testing.T, limit, grace time.Duration) {
	t.Helper()
	oldLimit, oldGrace := gitReadLimit, gitReadEndGrace
	gitReadLimit, gitReadEndGrace = limit, grace
	t.Cleanup(func() { gitReadLimit, gitReadEndGrace = oldLimit, oldGrace })
}

// spawnHelperGitRead starts this test binary in the helper mode as the pane's git
// of a gitRead spawn with the id gr. The step gives args and the mode.
func spawnHelperGitRead(t *testing.T, m *Manager, c *conn.Conn, mode string, args ...string) *Proc {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	step := func(env []string) ([]string, []string, func(), error) {
		return args, append(env, "CLAUSTRUM_TEST_HELPER="+mode), func() {}, nil
	}
	p, err := m.spawnWith(c, "gr", exe, nil, "", nil, false, nil, 0, TempRootAsk{}, nil, &gitReadStart{step: step}, nil)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	return p
}

// TestGitReadLimitValues pins the limit of the pane's git at 120 s and the wait
// after its SIGTERM at 7 s (c9125eb8, measured on Linux and macOS, the limit on
// Windows too).
func TestGitReadLimitValues(t *testing.T) {
	if gitReadLimit != 120*time.Second || gitReadEndGrace != 7*time.Second {
		t.Errorf("limit %v and wait %v, want 2m0s and 7s", gitReadLimit, gitReadEndGrace)
	}
}

// TestPlainChildHasNoGitReadLimit: a plain child is not ended at the limit of the
// pane's git (c9125eb8, measured on Linux, macOS and Windows). The limit is
// shortened, and the child runs well past it with no signal.
func TestPlainChildHasNoGitReadLimit(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	setGitReadLimitForTest(t, 100*time.Millisecond, 100*time.Millisecond)
	sent := recordSignalsByProc(t)
	exe, env := helperCommand(t, "sleep")
	c, _ := pipeConn(t)
	p, err := m.Spawn(c, "plain", exe, []string{"60"}, "", env, false)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	for end := time.Now().Add(time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if got := sent(p.cmd.Process); len(got) != 0 || !p.IsLive() {
			t.Fatalf("the plain child got %q, live %v, want no signal", got, p.IsLive())
		}
	}
	endOwnChild(t, p)
}

// TestGitReadLimitStartsNoEndAfterTheShutdownBegan: once the shutdown began, the
// limit sends no signal and logs no line (claustrum's own, not measured). The
// limit is shortened, and the git runs past it.
func TestGitReadLimitStartsNoEndAfterTheShutdownBegan(t *testing.T) {
	logs := captureLogBuf(t)
	m := newTestProcManager(t)
	setGitReadLimitForTest(t, 300*time.Millisecond, 100*time.Millisecond)
	sent := recordSignalsByProc(t)
	c, _ := pipeConn(t)
	p := spawnHelperGitRead(t, m, c, "sleep", "60")
	m.RefuseStarts()
	for end := time.Now().Add(time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if got := sent(p.cmd.Process); len(got) != 0 || !p.IsLive() {
			t.Fatalf("the git got %q, live %v, want no signal after the shutdown began", got, p.IsLive())
		}
	}
	if strings.Contains(logs.String(), "the pane's git has run for") {
		t.Errorf("log = %q, want no limit line after the shutdown began", logs.String())
	}
	endOwnChild(t, p)
}
