//go:build linux

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/schubydoo/claustrum/internal/childrec"
)

// claudeCodeHelper is the helper in mode under a folder named ccd-cli, so the spawn
// counts as Claude Code (claudeCodeShape), with the two env names of a hostCreds
// spawn set to 1.
func claudeCodeHelper(t *testing.T, mode string) (string, map[string]string) {
	t.Helper()
	exe, env := helperCommand(t, mode)
	dir := filepath.Join(t.TempDir(), "ccd-cli")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "claude")
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	env[hostManagedName] = "1"
	env[hostRefreshName] = "1"
	return link, env
}

// TestSocketHostCredsRefusals pins the refusals of a hostCreds spawn on Linux
// (13047a51, measured on Linux). A gitRead spawn of git gets the combine text, not
// the gitRead text and not the Claude Code text. A pty object with no key keeps its
// own text.
func TestSocketHostCredsRefusals(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	cc, env := claudeCodeHelper(t, "echo")
	plain, _ := helperCommand(t, "echo")
	refused := func(msg string) string {
		return `{"code":-32602,"message":"` + msg + `","data":{"reason":"other"}}`
	}
	const (
		empty   = "hostCreds refused: its env is empty"
		name    = "hostCreds refused: a name is empty or holds = or NUL"
		size    = "hostCreds refused: its names and values come to over 32 KiB"
		combine = "hostCreds refused: it came with pty, linkBound, gitRead or launcher"
		dup     = "hostCreds refused: a name is also in the spawn's env"
		notCC   = "hostCreds refused: the program is not Claude Code, which alone reads it"
		flags   = "hostCreds refused: the spawn's env does not set CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST and CLAUDE_CODE_SDK_HAS_HOST_AUTH_REFRESH to 1"
	)
	with := func(k, v string) map[string]string {
		e := map[string]string{}
		for n, val := range env {
			e[n] = val
		}
		if v == "" {
			delete(e, k)
		} else {
			e[k] = v
		}
		return e
	}
	big, _ := json.Marshal(strings.Repeat("x", 70000))
	cases := []struct {
		name    string
		command string
		env     map[string]string
		hc      string
		extra   map[string]any
		want    string
	}{
		{"empty object", cc, env, `{}`, nil, refused(empty)},
		{"empty env", cc, env, `{"env":{}}`, nil, refused(empty)},
		{"empty name", cc, env, `{"env":{"":"x"}}`, nil, refused(name)},
		{"name with =", cc, env, `{"env":{"A=B":"x"}}`, nil, refused(name)},
		{"name with NUL", cc, env, `{"env":{"F\u0000O":"a"}}`, nil, refused(name)},
		{"big value", cc, env, `{"env":{"FOO":` + string(big) + `}}`, nil, refused(size)},
		{"gitRead", "git", nil, `{"env":{"FOO":"1"}}`, map[string]any{"gitRead": true, "cwd": t.TempDir(), "args": []string{"rev-parse", "--show-toplevel"}}, refused(combine)},
		{"linkBound", cc, env, `{"env":{"FOO":"1"}}`, map[string]any{"linkBound": map[string]any{"linkId": "0123456789abcdef"}}, refused(combine)},
		{"pty with no key", cc, env, `{"env":{"FOO":"1"}}`, map[string]any{"pty": map[string]any{"cols": 80, "rows": 24}}, `{"code":-32602,"message":"A terminal needs a key of 32 bytes in hex"}`},
		{"name in env", cc, with("FOO", "base"), `{"env":{"FOO":"1"}}`, nil, refused(dup)},
		{"not Claude Code", plain, env, `{"env":{"FOO":"1"}}`, nil, refused(notCC)},
		{"flags missing", cc, with(hostManagedName, ""), `{"env":{"FOO":"1"}}`, nil, refused(flags)},
		{"flag 0", cc, with(hostRefreshName, "0"), `{"env":{"FOO":"1"}}`, nil, refused(flags)},
	}
	for i, c := range cases {
		got := string(cl.call(hostCredsReq(t, i+1, "hc-r"+strconv.Itoa(i), c.command, c.env, c.hc, c.extra)))
		want := `{"jsonrpc":"2.0","id":` + itoa(i+1) + `,"error":` + c.want + `}`
		if got != want {
			t.Errorf("%s: reply = %s\nwant %s", c.name, got, want)
		}
	}
}

// TestSocketHostCredsPipe pins what a Claude Code child of a hostCreds spawn reads
// (13047a51, measured on Linux): CLAUDE_CODE_HOST_CREDS_FILE=/proc/self/fd/99, a pipe
// there with the env in name order, a null expiresAt, a pid and a procStart, and no
// name of that env in its environment. A spawn with no hostCreds gets no variable.
// Names with a space or a leading digit and a NUL in a value pass (measured).
func TestSocketHostCredsPipe(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	cc, env := claudeCodeHelper(t, "host-creds")
	ok := `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`
	if got := string(cl.call(hostCredsReq(t, 1, "hc-ok", cc, env, `{"env":{"FOO":"secret1","BAR":"two words"}}`, map[string]any{"args": []string{"FOO"}}))); got != ok {
		t.Fatalf("spawn = %s\nwant %s", got, ok)
	}
	out := streamOf(t, cl.waitExit("hc-ok"), "stdout")
	var pid int
	for _, l := range strings.Split(out, "\n") {
		if v, found := strings.CutPrefix(l, "pid="); found {
			pid, _ = strconv.Atoi(v)
		}
	}
	if pid <= 0 {
		t.Fatalf("no pid in the output of the child:\n%s", out)
	}
	wantPayload := `{"env":{"BAR":"two words","FOO":"secret1"},"expiresAt":null,"pid":` + strconv.Itoa(pid) + `,"procStart":`
	if !strings.Contains(out, "var=/proc/self/fd/99\nin env=false\n") || !strings.Contains(out, "payload="+wantPayload) {
		t.Fatalf("child output:\n%s\nwant var=/proc/self/fd/99, in env=false and payload=%s<n>}", out, wantPayload)
	}
	start, _ := strconv.ParseInt(strings.TrimSuffix(strings.SplitN(out, `"procStart":`, 2)[1], "}\n"), 10, 64)
	if self := childrec.OwnStartTicks(); start <= 0 || start < self {
		t.Errorf("procStart = %d, want the start of the child, at or after %d", start, self)
	}
	if got := string(cl.call(hostCredsReq(t, 2, "hc-odd", cc, env, `{"env":{"A B":"x","1A":"y","V":"a\u0000b"}}`, map[string]any{"args": []string{"V"}}))); !strings.Contains(got, `"success":true`) {
		t.Errorf("spawn with odd names = %s, want success", got)
	}
	cl.waitExit("hc-odd")
	exe, plainEnv := helperCommand(t, "host-creds")
	params := map[string]any{"id": "hc-none", "command": exe, "args": []string{"FOO"}, "env": plainEnv}
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "process.spawn", "auth": testToken, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(cl.call(string(b))); !strings.Contains(got, `"success":true`) {
		t.Fatalf("spawn with no hostCreds = %s", got)
	}
	if out := streamOf(t, cl.waitExit("hc-none"), "stdout"); !strings.HasPrefix(out, "var=\n") {
		t.Errorf("child of a spawn with no hostCreds:\n%s\nwant an empty var", out)
	}
}
