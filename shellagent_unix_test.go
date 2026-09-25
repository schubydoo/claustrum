//go:build unix

package main

import (
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
	want := "WARN  [shellenv] A connection attempt to SSH_AUTH_SOCK=/tmp/hung.sock never returned (a hung filesystem?); " +
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

func shrinkAgentProbe(t *testing.T, timeout time.Duration) {
	t.Helper()
	oldT, oldB := agentProbeTimeout, agentProbeBound
	agentProbeTimeout, agentProbeBound = timeout, timeout+time.Second
	t.Cleanup(func() { agentProbeTimeout, agentProbeBound = oldT, oldB })
}

func TestAskLoginShellForAgent(t *testing.T) {
	long := strings.Repeat("0", 250)
	// markerOf sets $m to the fresh marker the probe script carries in "$4".
	markerOf := `m=$(printf '%s' "$4" | grep -o '___CLAUDE_SSH_AGENT_SOCK_[0-9a-f]*___'); `
	t.Setenv("SSH_TTY", "/dev/pts/9")
	t.Setenv("CLAUDE_SSH_PROBE_TEST", "set")
	cases := []struct {
		name, body, want, wantErr string
	}{
		{"exported path", `SSH_AUTH_SOCK=/tmp/x.sock; export SSH_AUTH_SOCK; exec /bin/sh -c "$4"`, "/tmp/x.sock", ""},
		{"not exported", `unset SSH_AUTH_SOCK; exec /bin/sh -c "$4"`, "", ""},
		{"a good line then a hang", `SSH_AUTH_SOCK=/tmp/y.sock; export SSH_AUTH_SOCK; /bin/sh -c "$4"; sleep 5`, "/tmp/y.sock", ""},
		{"not a path", `SSH_AUTH_SOCK=agent; export SSH_AUTH_SOCK; exec /bin/sh -c "$4"`, "",
			"printed something other than a path for SSH_AUTH_SOCK; output began: agent"},
		{"exit with output", `echo partial-out; echo errline >&2; exit 3`, "",
			"exited (exit status 3) without printing SSH_AUTH_SOCK; output began: partial-out\nerrline"},
		{"clean exit, nothing printed", `exit 0`, "", "did not print SSH_AUTH_SOCK"},
		{"output cut at 200 bytes", `printf ` + long, "",
			"did not print SSH_AUTH_SOCK; output began: " + long[:200] + "..."},
		{"deadline", `sleep 5`, "", "did not finish within 300ms"},
		{"stderr between the marker and the path", markerOf + `printf '%s\n' "$m"; echo noise >&2; echo /tmp/z.sock`,
			"/tmp/z.sock", ""},
		{"marker only on stderr", markerOf + `printf '%s\n/tmp/z.sock\n' "$m" >&2`, "",
			"did not print SSH_AUTH_SOCK; output began: ___MARKER___\n/tmp/z.sock"},
		{"exactly 200 bytes is not cut", `printf ` + long[:200], "",
			"did not print SSH_AUTH_SOCK; output began: " + long[:200]},
		{"probe env reaches the shell",
			`SSH_AUTH_SOCK="/env/$CLAUDE_DESKTOP_RESOLVING_ENVIRONMENT/${SSH_TTY:-none}/${CLAUDE_SSH_PROBE_TEST:-none}"; ` +
				`export SSH_AUTH_SOCK; exec /bin/sh -c "$4"`, "/env/1/none/none", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shell := fakeLoginShell(t, tc.body)
			shrinkAgentProbe(t, 300*time.Millisecond)
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

func TestDialAgentSocket(t *testing.T) {
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
	release := make(chan struct{})
	defer close(release)
	old, oldBound := dialUnix, agentDialBound
	dialUnix = func(string, string, time.Duration) (net.Conn, error) {
		<-release
		return nil, errors.New("released")
	}
	agentDialBound = 50 * time.Millisecond
	t.Cleanup(func() { dialUnix, agentDialBound = old, oldBound })
	if live, hung := dialAgentSocket("/stuck/agent.sock"); live || !hung {
		t.Errorf("live=%v hung=%v, want a dial that never returns to read as hung", live, hung)
	}
}

// TestSuiteStubsShellAgentSocket guards TestMain's stub: without it, any test
// that spawns a fixture runs the user's real login shell.
func TestSuiteStubsShellAgentSocket(t *testing.T) {
	ran := filepath.Join(t.TempDir(), "ran")
	fakeLoginShell(t, `touch '`+ran+`'; SSH_AUTH_SOCK=/tmp/real.sock; export SSH_AUTH_SOCK; exec /bin/sh -c "$4"`)
	if got := shellAgentSocket(); got != "" {
		t.Errorf("shellAgentSocket() = %q, want the suite stub's empty answer", got)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Error("a login shell ran: TestMain no longer stubs the lookup")
	}
}
