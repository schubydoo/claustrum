//go:build unix

package shellenv

import (
	"context"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// agentLog collects the daemon log for one test. It keeps only the
// [shellenv] lines, because a goroutine that another test started can still log
// into the process-global logger while this test runs.
type agentLog struct{ buf *syncBuffer }

func captureAgentLog(t *testing.T) agentLog {
	t.Helper()
	buf := &syncBuffer{}
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldW); log.SetFlags(oldF) })
	return agentLog{buf}
}

func (l agentLog) Reset() { l.buf.Reset() }

func (l agentLog) String() string {
	var out strings.Builder
	for _, line := range strings.SplitAfter(l.buf.String(), "\n") {
		if strings.Contains(line, "[shellenv]") {
			out.WriteString(line)
		}
	}
	return out.String()
}

// fakeHandOff is an agentHandOff with a scripted probe, a manual clock and
// a table of which paths accept connections.
type fakeHandOff struct {
	*agentHandOff
	clock   time.Time
	answers []string // one per probe: a path, "" (exports none) or "!err"
	probes  int
	live    map[string]bool
	hung    map[string]bool
}

func newFakeHandOff(answers ...string) *fakeHandOff {
	f := &fakeHandOff{clock: time.Unix(1_000_000, 0), answers: answers,
		live: map[string]bool{}, hung: map[string]bool{}}
	f.agentHandOff = &agentHandOff{
		clock: func() time.Time { return f.clock },
		runShell: func() (string, error) {
			a := f.answers[f.probes]
			f.probes++
			if a == "!err" {
				return "", errors.New("login shell /bin/fake did not print SSH_AUTH_SOCK")
			}
			return a, nil
		},
		dial: func(p string) (bool, bool) { return f.live[p], f.hung[p] },
		gate: make(chan struct{}, 1),
	}
	return f
}

func TestHandOffLiveSocketIsProbedOnce(t *testing.T) {
	logs := captureAgentLog(t)
	f := newFakeHandOff("/tmp/a.sock")
	f.live["/tmp/a.sock"] = true
	for i := 0; i < 3; i++ {
		if got := f.socket(); got != "/tmp/a.sock" {
			t.Fatalf("call %d: got %q", i, got)
		}
	}
	if f.probes != 1 {
		t.Errorf("probes = %d, want 1 while the socket stays live", f.probes)
	}
	if want := "INFO  [shellenv] The login shell exports SSH_AUTH_SOCK=/tmp/a.sock\n"; logs.String() != want {
		t.Errorf("log = %q, want %q", logs.String(), want)
	}
}

func TestHandOffDeadSocketWaitsForReprobe(t *testing.T) {
	logs := captureAgentLog(t)
	f := newFakeHandOff("/tmp/a.sock", "/tmp/a.sock")
	f.live["/tmp/a.sock"] = true
	f.socket()
	logs.Reset()

	f.live["/tmp/a.sock"] = false // the agent died
	f.clock = f.clock.Add(5 * time.Second)
	if got := f.socket(); got != "" || f.probes != 1 || logs.String() != "" {
		t.Fatalf("before 10m: got %q, probes %d, log %q; want silence and no probe", got, f.probes, logs)
	}

	f.live["/tmp/a.sock"] = true // restarted at the same path
	if got := f.socket(); got != "/tmp/a.sock" || f.probes != 1 {
		t.Fatalf("restarted agent: got %q, probes %d; want the cached path with no probe", got, f.probes)
	}

	f.live["/tmp/a.sock"] = false
	f.clock = f.lastRun.Add(agentReprobeAfter - time.Second)
	if got := f.socket(); got != "" || f.probes != 1 {
		t.Fatalf("1s before 10m: got %q, probes %d; want no probe yet", got, f.probes)
	}
	f.clock = f.lastRun.Add(agentReprobeAfter)
	if got := f.socket(); got != "" || f.probes != 2 {
		t.Fatalf("after 10m: got %q, probes %d; want a second probe", got, f.probes)
	}
	want := "WARN  [shellenv] SSH_AUTH_SOCK=/tmp/a.sock no longer accepts connections; running the login shell again\n" +
		"WARN  [shellenv] The login shell exports SSH_AUTH_SOCK=/tmp/a.sock, which does not accept connections " +
		"(try 1 of 3; will try again in 10m0s at the earliest)\n"
	if logs.String() != want {
		t.Errorf("log = %q\nwant  %q", logs.String(), want)
	}
}

func TestHandOffThreeStrikes(t *testing.T) {
	logs := captureAgentLog(t)
	f := newFakeHandOff("", "", "", "/tmp/never.sock")
	for i := 0; i < 4; i++ {
		if got := f.socket(); got != "" {
			t.Fatalf("call %d: got %q", i, got)
		}
		if i == 0 {
			// Not due yet: a second call right away runs nothing.
			f.socket()
		}
		f.clock = f.clock.Add(agentReprobeAfter)
	}
	if f.probes != 3 {
		t.Errorf("probes = %d, want 3 then no more", f.probes)
	}
	want := "INFO  [shellenv] The login shell exports no SSH_AUTH_SOCK (try 1 of 3; will try again in 10m0s at the earliest)\n" +
		"INFO  [shellenv] The login shell exports no SSH_AUTH_SOCK (try 2 of 3; will try again in 10m0s at the earliest)\n" +
		"INFO  [shellenv] The login shell exports no SSH_AUTH_SOCK (try 3 of 3; not running it again)\n"
	if logs.String() != want {
		t.Errorf("log = %q\nwant  %q", logs.String(), want)
	}
}

func TestHandOffSuccessResetsFailures(t *testing.T) {
	f := newFakeHandOff("", "", "/tmp/a.sock", "", "", "")
	f.live["/tmp/a.sock"] = true
	for i := 0; i < 3; i++ {
		f.socket()
		f.clock = f.clock.Add(agentReprobeAfter)
	}
	if f.failures != 0 {
		t.Fatalf("failures = %d after a success, want 0", f.failures)
	}
	f.live["/tmp/a.sock"] = false
	for i := 0; i < 4; i++ {
		f.socket()
		f.clock = f.clock.Add(agentReprobeAfter)
	}
	if f.probes != 6 {
		t.Errorf("probes = %d, want 6: a success gives a fresh budget of 3", f.probes)
	}
}

func TestHandOffProbeErrorKeepsCachedPath(t *testing.T) {
	logs := captureAgentLog(t)
	f := newFakeHandOff("/tmp/a.sock", "!err")
	f.live["/tmp/a.sock"] = true
	f.socket()
	logs.Reset()
	f.live["/tmp/a.sock"] = false
	f.clock = f.clock.Add(agentReprobeAfter)
	f.socket()
	if f.sock != "/tmp/a.sock" {
		t.Errorf("sock = %q, want the old path kept after a failed probe", f.sock)
	}
	if !strings.Contains(logs.String(), "WARN  [shellenv] Could not read SSH_AUTH_SOCK from the login shell "+
		"(try 1 of 3; will try again in 10m0s at the earliest): login shell /bin/fake did not print SSH_AUTH_SOCK\n") {
		t.Errorf("log = %q", logs.String())
	}
}

func TestHandOffHungSocketEndsTheHandOff(t *testing.T) {
	logs := captureAgentLog(t)
	f := newFakeHandOff("/tmp/hung.sock", "/tmp/other.sock")
	f.hung["/tmp/hung.sock"] = true
	if got := f.socket(); got != "" {
		t.Fatalf("got %q", got)
	}
	f.clock = f.clock.Add(24 * time.Hour)
	if got := f.socket(); got != "" || f.probes != 1 {
		t.Fatalf("after a hang: got %q, probes %d; want no further probe", got, f.probes)
	}
	want := "WARN  [shellenv] A check of SSH_AUTH_SOCK=/tmp/hung.sock never returned (a hung filesystem?); " +
		"not using it or asking the login shell again for this daemon's lifetime\n"
	if logs.String() != want {
		t.Errorf("log = %q\nwant  %q", logs.String(), want)
	}
}

func TestHandOffCachedSocketHangs(t *testing.T) {
	f := newFakeHandOff("/tmp/a.sock")
	f.live["/tmp/a.sock"] = true
	f.socket()
	f.live["/tmp/a.sock"] = false
	f.hung["/tmp/a.sock"] = true
	if got := f.socket(); got != "" || f.sock != "" || f.failures != agentMaxFailures {
		t.Errorf("got %q, sock %q, failures %d; want the hand-off ended", got, f.sock, f.failures)
	}
}

var markerRE = regexp.MustCompile(`___CLAUDE_SSH_AGENT_SOCK_[0-9a-f]{32}___`)

// fakeLoginShell installs an executable script as $SHELL. The probe runs it as
// `<shell> -l -i -c <script>`, so "$4" is the probe's own script.
func fakeLoginShell(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fakeshell")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", p)
	return p
}

// shrinkAgentProbe sets the probe deadline. The bound is a minute past it, so a
// slow start on a loaded runner does not read as a run that could not be stopped.
func shrinkAgentProbe(t *testing.T, timeout time.Duration) {
	t.Helper()
	oldT, oldB := agentProbeTimeout, agentProbeBound
	agentProbeTimeout, agentProbeBound = timeout, timeout+time.Minute
	t.Cleanup(func() { agentProbeTimeout, agentProbeBound = oldT, oldB })
}

// A slow runner can start the fake shell after a short deadline. Each case of
// TestAskLoginShellForAgent ends by itself, so it gets this deadline, which it never reaches.
const agentProbeTimeoutFar = time.Minute

func TestAskLoginShellForAgent(t *testing.T) {
	long := strings.Repeat("0", 250)
	shrinkShellCaps(t)
	// markerOf sets $m to the fresh marker the probe script carries in "$4".
	markerOf := `m=$(printf '%s' "$4" | grep -o '___CLAUDE_SSH_AGENT_SOCK_[0-9a-f]*___'); `
	t.Setenv("SSH_TTY", "/dev/pts/9")
	t.Setenv("CLAUDE_SSH_PROBE_TEST", "set")
	cases := []struct {
		name, body, want, wantErr string
	}{
		{"exported path", `SSH_AUTH_SOCK=/tmp/x.sock; export SSH_AUTH_SOCK; exec /bin/sh -c "$4"`, "/tmp/x.sock", ""},
		{"not exported", `unset SSH_AUTH_SOCK; exec /bin/sh -c "$4"`, "", ""},
		{"not a path", `SSH_AUTH_SOCK=agent; export SSH_AUTH_SOCK; exec /bin/sh -c "$4"`, "",
			"printed something other than a path for SSH_AUTH_SOCK (67 bytes on stdout, 0 on stderr)"},
		{"exit with output", `echo partial-out; echo errline >&2; exit 3`, "",
			"exited (exit status 3) without printing SSH_AUTH_SOCK (12 bytes on stdout, 8 on stderr)"},
		{"clean exit, nothing printed", `exit 0`, "", "did not print SSH_AUTH_SOCK (0 bytes on stdout, 0 on stderr)"},
		{"stdout over its cap", `printf ` + long, "",
			"exited (cut off after it had printed more than 1 MiB on stdout or 16 MiB on stderr) without printing SSH_AUTH_SOCK (128 bytes on stdout, 0 on stderr)"},
		{"stderr over its keep", `printf '%080d' 0 >&2`, "", "did not print SSH_AUTH_SOCK (0 bytes on stdout, 32 on stderr)"},
		{"stderr between the marker and the path", markerOf + `printf '%s\n' "$m"; echo noise >&2; echo /tmp/z.sock`,
			"/tmp/z.sock", ""},
		{"marker only on stderr", markerOf + `printf '%s\n/tmp/z.sock\n' "$m" >&2`, "",
			"did not print SSH_AUTH_SOCK (0 bytes on stdout, 32 on stderr)"},
		{"exactly the stdout cap is not cut", `printf ` + long[:128], "",
			"did not print SSH_AUTH_SOCK (128 bytes on stdout, 0 on stderr)"},
		{"probe env reaches the shell",
			`SSH_AUTH_SOCK="/env/$CLAUDE_DESKTOP_RESOLVING_ENVIRONMENT/${SSH_TTY:-none}/${CLAUDE_SSH_PROBE_TEST:-none}"; ` +
				`export SSH_AUTH_SOCK; exec /bin/sh -c "$4"`, "/env/1/none/none", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shell := fakeLoginShell(t, tc.body)
			shrinkAgentProbe(t, agentProbeTimeoutFar)
			got, err := askLoginShellForAgent()
			if tc.wantErr == "" {
				if err != nil || got != tc.want {
					t.Fatalf("got %q, %v; want %q", got, err, tc.want)
				}
				return
			}
			// The marker is random per run, so a message that echoes it is compared
			// with the marker masked.
			msg := ""
			if err != nil {
				msg = markerRE.ReplaceAllString(err.Error(), "___MARKER___")
			}
			if want := "login shell " + shell + " " + tc.wantErr; msg != want {
				t.Fatalf("err = %v\nwant  %s", err, want)
			}
		})
	}
}

// A shell that does not end gets the deadline error, and the kill ends its group.
// The shell writes its pid, which is its group id, as its first action. Its
// grandchild then writes its own pid and group id. A try whose deadline came
// before both writes proves nothing, and the next try gets four times the
// deadline. The after file catches a call that waits for the whole group to end.
// groupGone and the poll of the grandchild check that the kill ended the group.
func TestAskLoginShellForAgentDeadline(t *testing.T) {
	shrinkShellCaps(t)
	shell := fakeLoginShell(t, `echo $$ > "$FAKESHELL_PGID"; `+
		`( /bin/sh -c 'pg=$(ps -o pgid= -p $$); echo "$$ $pg" > "$FAKESHELL_CHILD"; exec sleep 30'; `+
		`: > "$FAKESHELL_AFTER" )`)
	for timeout := 300 * time.Millisecond; timeout <= 20*time.Second; timeout *= 4 {
		after := filepath.Join(t.TempDir(), "after")
		pgidFile := filepath.Join(t.TempDir(), "pgid")
		childFile := filepath.Join(t.TempDir(), "child")
		t.Setenv("FAKESHELL_AFTER", after)
		t.Setenv("FAKESHELL_PGID", pgidFile)
		t.Setenv("FAKESHELL_CHILD", childFile)
		shrinkAgentProbe(t, timeout)
		got, err := askLoginShellForAgent()
		if _, statErr := os.Lstat(after); statErr == nil {
			t.Fatal("the call waited for the whole group of the fake shell to end")
		}
		// A kill between the open and the write of a file leaves it empty.
		pgidText, _ := os.ReadFile(pgidFile)
		childText, _ := os.ReadFile(childFile)
		childFields := strings.Fields(string(childText))
		if len(strings.TrimSpace(string(pgidText))) == 0 || len(childFields) != 2 {
			note := ""
			if timeout*4 <= 20*time.Second {
				note = ", trying again"
			}
			t.Logf("the %v deadline came before the first lines of %s (got %q, %v)%s", timeout, shell, got, err, note)
			continue
		}
		pgid, convErr := strconv.Atoi(strings.TrimSpace(string(pgidText)))
		if convErr != nil || pgid <= 1 {
			t.Fatalf("the fake shell wrote the group id %q", pgidText)
		}
		child, childErr := strconv.Atoi(childFields[0])
		childGroup, groupErr := strconv.Atoi(childFields[1])
		if childErr != nil || groupErr != nil || child <= 1 {
			t.Fatalf("the grandchild wrote %q", childText)
		}
		if childGroup != pgid {
			t.Fatalf("the grandchild %d is in the group %d, want the group %d of the fake shell", child, childGroup, pgid)
		}
		groupGone(t, pgid)
		// Signal 0 sends nothing. It only asks whether our own grandchild is still there.
		for deadline := time.Now().Add(10 * time.Second); syscall.Kill(child, 0) == nil; time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("the grandchild %d of the fake shell is still there", child)
			}
		}
		if want := "login shell " + shell + " did not finish within " + timeout.String(); got != "" || err == nil || err.Error() != want {
			t.Fatalf("got %q, %v\nwant  %s", got, err, want)
		}
		return
	}
	t.Fatal("the fake shell and its grandchild never wrote their ids before the deadline")
}

// A good line wins after the deadline ended the shell. The deadline has to fire
// after the line, so the shell writes a ready file right after it. A try whose
// deadline came first proves nothing, and the next try gets four times the deadline.
func TestAskLoginShellForAgentGoodLineThenHang(t *testing.T) {
	shrinkShellCaps(t)
	shell := fakeLoginShell(t, `SSH_AUTH_SOCK=/tmp/y.sock; export SSH_AUTH_SOCK; /bin/sh -c "$4"; `+
		`: > "$FAKESHELL_READY"; sleep 30`)
	for timeout := 300 * time.Millisecond; timeout <= 20*time.Second; timeout *= 4 {
		ready := filepath.Join(t.TempDir(), "ready")
		t.Setenv("FAKESHELL_READY", ready)
		shrinkAgentProbe(t, timeout)
		got, err := askLoginShellForAgent()
		if err == nil && got == "/tmp/y.sock" {
			return
		}
		if _, statErr := os.Lstat(ready); statErr == nil {
			t.Fatalf("got %q, %v; want %q: the line came before the %v deadline", got, err, "/tmp/y.sock", timeout)
		}
		note := ""
		if timeout*4 <= 20*time.Second {
			note = ", trying again"
		}
		t.Logf("the %v deadline came before the line of %s (got %q, %v)%s", timeout, shell, got, err, note)
	}
	t.Fatal("the fake shell never printed its line before the deadline")
}

func TestProbeShellEnv(t *testing.T) {
	got := probeShellEnv([]string{
		"HOME=/h", "CLAUDE_SSH_RUN_DIR=/r", "CLAUDE_SSH_CHILD=x", "SSH_CONNECTION=1 2 3 4",
		"SSH_CLIENT=1 2 3", "SSH_TTY=/dev/pts/1", "SSH_AUTH_SOCK=/keep", "DISABLE_AUTO_UPDATE=false",
		"CLAUDE_RPC_TOKEN=t",
	})
	want := []string{"HOME=/h", "SSH_AUTH_SOCK=/keep", "DISABLE_AUTO_UPDATE=true", "CLAUDE_RPC_TOKEN=t",
		"ZSH_DISABLE_COMPFIX=true", "CLAUDE_DESKTOP_RESOLVING_ENVIRONMENT=1"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("env = %q\nwant  %q", got, want)
	}
}

// listenAgentSock listens on a unix socket in a short directory: macOS caps a
// socket path near 104 bytes, and its t.TempDir paths are long.
func listenAgentSock(t *testing.T, name string) (string, net.Listener) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "agsk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, name)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	return sock, ln
}

// resetAgentLive clears the live-answer cache, and again at the end.
func resetAgentLive(t *testing.T) {
	t.Helper()
	clear := func() {
		agentLive.Lock()
		agentLive.present = false
		agentLive.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

// countDials counts the connects of dialAgentSocket and ends with the test.
func countDials(t *testing.T) *int {
	t.Helper()
	var n int
	old := dialUnix
	dialUnix = func(netw, addr string, d time.Duration) (net.Conn, error) {
		n++
		return old(netw, addr, d)
	}
	t.Cleanup(func() { dialUnix = old })
	return &n
}

func TestDialAgentSocketHungStat(t *testing.T) {
	resetAgentLive(t)
	release := make(chan struct{})
	defer close(release)
	old, oldBound := statAgent, agentDialBound
	statAgent = func(string) (os.FileInfo, error) { <-release; return nil, errors.New("released") }
	agentDialBound = 50 * time.Millisecond
	t.Cleanup(func() { statAgent, agentDialBound = old, oldBound })
	type answer struct{ live, hung bool }
	done := make(chan answer, 1)
	go func() {
		live, hung := dialAgentSocket("/stuck/agent.sock")
		done <- answer{live, hung}
	}()
	select {
	case a := <-done:
		if a.live || !a.hung {
			t.Errorf("live=%v hung=%v, want a stat that never returns to read as hung", a.live, a.hung)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dialAgentSocket did not return within the bound")
	}
	f := newFakeHandOff("/stuck/agent.sock")
	f.dial = dialAgentSocket
	if got := f.socket(); got != "" || f.failures != agentMaxFailures {
		t.Errorf("got %q, failures %d, want the hand-off ended as for a hung dial", got, f.failures)
	}
}

func TestDialAgentSocketPlainFileNoDial(t *testing.T) {
	resetAgentLive(t)
	n := countDials(t)
	plain := filepath.Join(t.TempDir(), "a.sock")
	if err := os.WriteFile(plain, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if live, hung := dialAgentSocket(plain); live || hung || *n != 0 {
		t.Errorf("plain file: live=%v hung=%v dials=%d, want dead with no dial", live, hung, *n)
	}
}

func TestDialAgentSocketLiveAnswerIsCached(t *testing.T) {
	resetAgentLive(t)
	n := countDials(t)
	now := time.Unix(1_000_000, 0)
	old := agentLiveNow
	agentLiveNow = func() time.Time { return now }
	t.Cleanup(func() { agentLiveNow = old })
	sock, ln := listenAgentSock(t, "c.sock")
	if live, _ := dialAgentSocket(sock); !live || *n != 1 {
		t.Fatalf("first: live=%v dials=%d", live, *n)
	}
	// The agent dies and its file stays: Go unlinks on Close, so keep the file
	// by turning the unlink off.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	now = now.Add(agentLiveTTL - time.Second)
	if live, _ := dialAgentSocket(sock); !live || *n != 1 {
		t.Errorf("inside the expiry: live=%v dials=%d, want the cached answer with no dial", live, *n)
	}
	now = now.Add(2 * time.Second)
	if live, hung := dialAgentSocket(sock); live || hung || *n != 2 {
		t.Errorf("after the expiry: live=%v hung=%v dials=%d, want one more dial, refused", live, hung, *n)
	}
}

func TestDialAgentSocketNewSocketIsDialedAgain(t *testing.T) {
	resetAgentLive(t)
	n := countDials(t)
	sock, ln := listenAgentSock(t, "n.sock")
	if live, _ := dialAgentSocket(sock); !live || *n != 1 {
		t.Fatalf("first: live=%v dials=%d", live, *n)
	}
	_ = ln.Close() // unlinks the file
	ln2, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln2.Close()
	// A reused inode and a coarse mtime could leave the key as it was.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(sock, later, later); err != nil {
		t.Fatal(err)
	}
	if live, _ := dialAgentSocket(sock); !live || *n != 2 {
		t.Errorf("new socket: live=%v dials=%d, want a dial", live, *n)
	}
}

func TestDialAgentSocketTouchedSocketIsDialedAgain(t *testing.T) {
	resetAgentLive(t)
	n := countDials(t)
	sock, ln := listenAgentSock(t, "t.sock")
	if live, _ := dialAgentSocket(sock); !live || *n != 1 {
		t.Fatalf("first: live=%v dials=%d", live, *n)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(sock, later, later); err != nil {
		t.Fatal(err)
	}
	if live, _ := dialAgentSocket(sock); live || *n != 2 {
		t.Errorf("touched dead socket: live=%v dials=%d, want a refused dial", live, *n)
	}
}

func TestDialAgentSocket(t *testing.T) {
	resetAgentLive(t)
	// A short directory: macOS caps a unix socket path near 104 bytes, and its
	// t.TempDir paths are long.
	dir, err := os.MkdirTemp("/tmp", "agsk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "a.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	if live, hung := dialAgentSocket(sock); !live || hung {
		t.Errorf("listening socket: live=%v hung=%v", live, hung)
	}
	_ = ln.Close()
	if live, hung := dialAgentSocket(sock); live || hung {
		t.Errorf("closed socket: live=%v hung=%v", live, hung)
	}
	if live, hung := dialAgentSocket(""); live || hung {
		t.Errorf("empty path: live=%v hung=%v", live, hung)
	}
}

func TestWaitAtMostTimesOut(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	v, ok := waitAtMost(20*time.Millisecond, func() int { <-block; return 7 })
	if ok || v != 0 {
		t.Errorf("got %d, %v; want 0, false", v, ok)
	}
	if v, ok := waitAtMost(time.Second, func() int { return 7 }); !ok || v != 7 {
		t.Errorf("got %d, %v; want 7, true", v, ok)
	}
}

func TestDialAgentSocketHung(t *testing.T) {
	sock, ln := listenAgentSock(t, "h.sock")
	defer ln.Close()
	resetAgentLive(t)
	release := make(chan struct{})
	defer close(release)
	old, oldBound := dialUnix, agentDialBound
	dialUnix = func(string, string, time.Duration) (net.Conn, error) {
		<-release
		return nil, errors.New("released")
	}
	agentDialBound = 50 * time.Millisecond
	t.Cleanup(func() { dialUnix, agentDialBound = old, oldBound })
	if live, hung := dialAgentSocket(sock); live || !hung {
		t.Errorf("live=%v hung=%v, want a dial that never returns to read as hung", live, hung)
	}
}

// TestSuiteStubsBothLoginShellSteps guards the stubs of TestMain: without them, a
// test of this package that reaches either step runs the user's real login shell.
func TestSuiteStubsBothLoginShellSteps(t *testing.T) {
	ran := filepath.Join(t.TempDir(), "ran")
	fakeLoginShell(t, `touch '`+ran+`'; SSH_AUTH_SOCK=/tmp/real.sock; export SSH_AUTH_SOCK; exec /bin/sh -c "$4"`)
	if got := AgentSocket(); got != "" {
		t.Errorf("AgentSocket() = %q, want the suite stub's empty answer", got)
	}
	if _, err := LoginPATHExtractor(context.Background()); err == nil {
		t.Error("LoginPATHExtractor() gave a PATH, want the suite stub's error")
	}
	if _, err := os.Stat(ran); err == nil {
		t.Error("a login shell ran: TestMain no longer stubs both steps")
	}
}

// TestHandOffRunsNoShellWhileTheGateIsTaken pins 52d82f0a (Linux and macOS): while
// the PATH read runs, the hand-off runs no shell. claustrum's own, not measured: it
// counts no try and logs nothing.
func TestHandOffRunsNoShellWhileTheGateIsTaken(t *testing.T) {
	logs := captureAgentLog(t)
	f := newFakeHandOff("/tmp/a.sock", "/tmp/a.sock")
	f.live["/tmp/a.sock"] = true
	f.gate <- struct{}{}
	if got := f.socket(); got != "" || f.probes != 0 || f.failures != 0 || !f.lastRun.IsZero() || logs.String() != "" {
		t.Fatalf("taken gate: got %q, probes %d, failures %d, lastRun %v, log %q; want nothing",
			got, f.probes, f.failures, f.lastRun, logs)
	}
	<-f.gate
	if got := f.socket(); got != "/tmp/a.sock" || f.probes != 1 {
		t.Fatalf("free gate: got %q, probes %d; want the socket", got, f.probes)
	}
	if len(f.gate) != 0 {
		t.Error("the hand-off kept the gate after its run")
	}

	// A cached socket that died and is due logs its line only when the shell runs.
	f.live["/tmp/a.sock"] = false
	f.clock = f.clock.Add(agentReprobeAfter)
	logs.Reset()
	f.gate <- struct{}{}
	if got := f.socket(); got != "" || f.probes != 1 || logs.String() != "" {
		t.Fatalf("taken gate, dead socket: got %q, probes %d, log %q", got, f.probes, logs)
	}
	<-f.gate
}

// TestPATHReadHoldsTheGateOfTheHandOff pins 52d82f0a (Linux and macOS): a spawn during
// a running PATH read gets no SSH_AUTH_SOCK, and the agent shell runs after the read.
// Production gives both steps one gate.
func TestPATHReadHoldsTheGateOfTheHandOff(t *testing.T) {
	if agentHandOffState.gate != loginShellGate || loginPATHs.gate != loginShellGate {
		t.Fatal("the PATH read and the hand-off do not share loginShellGate")
	}
	p := installPathReadFake(t)
	f := newFakeHandOff("/tmp/a.sock")
	f.gate = loginPATHs.gate
	f.live["/tmp/a.sock"] = true
	WarmAtStart()
	r := startedRead(t, p)
	if got := f.socket(); got != "" || f.probes != 0 {
		t.Fatalf("during the read: got %q, probes %d; want no shell", got, f.probes)
	}
	p.answer <- pathAnswer{path: "/login/bin"}
	awaitReadEnd(t, r)
	if got := f.socket(); got != "/tmp/a.sock" || f.probes != 1 {
		t.Fatalf("after the read: got %q, probes %d; want the socket", got, f.probes)
	}
}

// TestHandOffFreesTheGateAfterAPanic: a panic in the agent run leaves the gate free.
func TestHandOffFreesTheGateAfterAPanic(t *testing.T) {
	f := newFakeHandOff()
	f.runShell = func() (string, error) { panic("boom") }
	func() {
		defer func() { _ = recover() }()
		f.socket()
	}()
	if len(f.gate) != 0 {
		t.Error("a panic in the agent run kept the gate")
	}
}
