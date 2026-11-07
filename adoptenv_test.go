package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/shellenv"
)

// clearClaudeSSHEnv removes each CLAUDE_SSH_ variable of this process until the
// test ends, so the counts of the adopt line hold on any host.
func clearClaudeSSHEnv(t *testing.T) {
	t.Helper()
	for _, e := range os.Environ() {
		name, value, _ := strings.Cut(e, "=")
		if name != "" && strings.HasPrefix(strings.ToUpper(name), "CLAUDE_SSH_") {
			t.Setenv(name, value)
			_ = os.Unsetenv(name)
		}
	}
}

// clearSessionEnv removes each session-bound variable of this process until the test
// ends, so a child holds only the session names that the test sets.
func clearSessionEnv(t *testing.T) {
	t.Helper()
	exact := []string{"DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY", "KRB5CCNAME", "XDG_SEAT", "XDG_VTNR", "GPG_TTY", "WSL_INTEROP"}
	for _, e := range os.Environ() {
		name, value, _ := strings.Cut(e, "=")
		up := strings.ToUpper(name)
		if name == "" || !slices.Contains(exact, up) && !strings.HasPrefix(up, "SSH_") && !strings.HasPrefix(up, "SSH2_") && !strings.HasPrefix(up, "XDG_SESSION_") {
			continue
		}
		t.Setenv(name, value)
		_ = os.Unsetenv(name)
	}
}

// adoptKeptCount is the count of the temp-folder names of the daemon that an adopt
// keeps (c9125eb8, measured on Linux, macOS and Windows).
func adoptKeptCount() int {
	n := 0
	for _, name := range []string{"TMPDIR", "TMP", "TEMP", "CLAUDE_CODE_TMPDIR"} {
		if _, ok := os.LookupEnv(name); ok {
			n++
		}
	}
	return n
}

// adoptLines are the lines of the log that server.adoptEnv wrote, without the
// level tag, the connection label replaced by <C>.
func adoptLines(log string) []string {
	var out []string
	for _, line := range strings.Split(log, "\n") {
		if i := strings.Index(line, "[ServerHandler] server.adoptEnv:"); i >= 0 {
			out = append(out, line[i:])
			continue
		}
		i := strings.Index(line, "[Server] New processes now start")
		if i < 0 {
			continue
		}
		line = line[i:]
		head, rest, ok := strings.Cut(line, "sent on connection ")
		if !ok {
			out = append(out, line)
			continue
		}
		_, tail, _ := strings.Cut(rest, ": ")
		out = append(out, head+"sent on connection <C>: "+tail)
	}
	return out
}

// TestSocketAdoptEnvFrames: the replies and the log lines of server.adoptEnv,
// byte for byte, after the rows of c9125eb8 (measured on Linux, macOS and Windows).
func TestSocketAdoptEnvFrames(t *testing.T) {
	clearClaudeSSHEnv(t)
	t.Setenv("CLAUDE_SSH_DAEMON_CHILD", "1")
	t.Setenv("CLAUSTRUM_TEST_KEEP", "daemon")
	_, sock := newRunningServer(t)
	logs := captureLogBuf(t)
	cl := dial(t, sock)

	ok := func(id int) string { return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"ok":true}}`, id) }
	bad := func(id int) string {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"error":{"code":-32602,"message":"Invalid params"}}`, id)
	}
	list := func(prefix string, n, size int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("%s%d=%s", prefix, i, strings.Repeat("y", size))
		}
		return out
	}
	params := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	big := func(n int) []string { return []string{"A=1", "BIG=" + strings.Repeat("x", n-4)} }
	rows := []struct {
		params string // "" sends no params member
		want   func(int) string
	}{
		{params(map[string]any{"env": []string{"A=1", "ZZ_ADOPT=hello", "HOME=/tmp/adopted-home", "PATH=/adopted/bin:/usr/bin:/bin", "SSH_AUTH_SOCK=/adopted/agent", "CLAUDE_SSH_X=1"}}), ok},
		{params(map[string]any{"env": []string{"B=2"}, "supplied": []string{"CONN_ONLY=x1"}}), ok},
		{params(map[string]any{"env": []string{"B=3"}, "noLoginShell": true}), ok},
		{params(map[string]any{"env": []string{}}), bad},
		{params(map[string]any{"env": []string{"A=1", "NOEQUALS"}}), bad},
		{params(map[string]any{"env": []string{"A=1", "N=a\x00b"}}), bad},
		{params(map[string]any{"env": big(131071)}), ok},
		{params(map[string]any{"env": big(131072)}), bad},
		{params(map[string]any{"env": list("V", 8193, 0)}), bad},
		{params(map[string]any{"env": list("V", 8192, 0)}), ok},
		{params(map[string]any{"env": list("V", 3, 100000)}), bad},
		{params(map[string]any{"env": list("V", 4, 65530)}), ok},
		{params(map[string]any{"env": []string{"Z=1"}, "kept": list("K", 8193, 0)}), bad},
		{`{"env":"A=1"}`, bad},
		{"", bad},
		{`null`, bad},
		{`{}`, bad},
		{`{"env":[1,2]}`, bad},
		{params(map[string]any{"env": []string{"A=final"}}), ok},
		{params(map[string]any{"env": []string{"a\x00b"}}), bad},
		{params(map[string]any{"env": append(list("V", 8192, 0), "NOEQUALS")}), bad},
		{params(map[string]any{"env": append(big(131072), list("V", 8193, 0)...)}), bad},
		{params(map[string]any{"env": []string{"A=1", "A=2", "B=1"}}), ok},
		{params(map[string]any{"env": []string{"Z=1"}, "kept": []string{"CLAUSTRUM_TEST_KEEP"}}), ok},
		{params(map[string]any{"env": []string{"CLAUSTRUM_TEST_KEEP=sent"}, "kept": []string{"CLAUSTRUM_TEST_KEEP"}}), ok},
	}
	for i, row := range rows {
		id := i + 1
		line := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"server.adoptEnv"}`, id)
		if row.params != "" {
			line = fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"server.adoptEnv","params":%s}`, id, row.params)
		}
		if got, want := string(cl.call(authed(line))), row.want(id); got != want {
			t.Errorf("row %d: reply %s, want %s", id, got, want)
		}
	}

	kept := adoptKeptCount()
	every := func(n int) string { return fmt.Sprintf("%d variables for every connection", n+1+kept) }
	refused := func(why string) string {
		return "[ServerHandler] server.adoptEnv: refused the environment that a new connection sent (" + why + "). New processes keep starting from the previous environment."
	}
	adopted := func(n, own int, names string) string {
		return "[Server] New processes now start from the environment sent on connection <C>: " + every(n) +
			fmt.Sprintf(", %d for this connection only. Names added, removed or changed (at most 40): %s", own, names)
	}
	vNames := make([]string, 0, 40)
	for _, n := range []string{"V0", "V1", "V10", "V100", "V1000", "V1001", "V1002", "V1003", "V1004", "V1005", "V1006", "V1007", "V1008", "V1009", "V101", "V1010", "V1011", "V1012", "V1013", "V1014", "V1015", "V1016", "V1017", "V1018", "V1019", "V102", "V1020", "V1021", "V1022", "V1023", "V1024", "V1025", "V1026", "V1027", "V1028", "V1029", "V103", "V1030", "V1031", "V1032"} {
		vNames = append(vNames, `"`+n+`"`)
	}
	want := []string{
		"", // the first line lists the names of the test process, which differ by host
		adopted(1, 0, `["A" "B" "HOME" "PATH" "ZZ_ADOPT"]`),
		adopted(1, 0, `["B"]`),
		refused("it is empty"),
		refused("an entry has no equals sign"),
		refused("an entry contains a NUL byte"),
		adopted(2, 0, `["A" "B" "BIG"]`),
		refused("one variable is 128 KiB or longer, more than Linux allows when it starts a program"),
		refused("it has more than 8192 entries"),
		adopted(8192, 0, `["A" "BIG" `+strings.Join(vNames[:38], " ")+`]`),
		refused("it is larger than 256 KiB in total"),
		adopted(4, 0, `[`+strings.Join(vNames, " ")+`]`),
		refused("it names more than 8192 variables to keep"),
		refused("it is empty"), // params null
		refused("it is empty"), // params {}
		adopted(1, 0, `["A" "V0" "V1" "V2" "V3"]`),
		refused("an entry contains a NUL byte"),
		refused("an entry has no equals sign"),
		refused("one variable is 128 KiB or longer, more than Linux allows when it starts a program"),
		adopted(2, 0, `["A" "B"]`),
		adopted(2, 0, `["A" "B" "CLAUSTRUM_TEST_KEEP" "Z"]`),
		adopted(1, 0, `["CLAUSTRUM_TEST_KEEP" "Z"]`),
	}
	got := adoptLines(logs.String())
	if len(got) != len(want) {
		t.Fatalf("got %d adopt lines, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	first := "[Server] New processes now start from the environment sent on connection <C>: " + every(4) + ", 1 for this connection only."
	if !strings.HasPrefix(got[0], first) {
		t.Errorf("line 1 = %q, want the start %q", got[0], first)
	}
	for i := 1; i < len(want); i++ {
		if got[i] != want[i] {
			t.Errorf("line %d = %q\nwant      %q", i+1, got[i], want[i])
		}
	}
}

// adoptChildEnv spawns the environ helper on cl as id and returns its environment.
func adoptChildEnv(t *testing.T, cl *testClient, id string, n int) map[string]string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := json.Marshal(map[string]any{"id": id, "command": exe, "env": map[string]string{"CLAUSTRUM_TEST_HELPER": "environ"}})
	if err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"process.spawn","params":%s}`, n, p)
	if got := string(cl.call(authed(line))); !strings.Contains(got, `"success":true`) {
		t.Fatalf("spawn %s: %s", id, got)
	}
	env := map[string]string{}
	for _, e := range strings.Split(streamBytes(t, cl.waitExit(id), "stdout"), "\n") {
		if name, value, ok := strings.Cut(e, "="); ok && name != "" {
			env[strings.ToUpper(name)] = value
		}
	}
	return env
}

// TestSocketAdoptEnvReachesChildren: after a good adopt on c1 a new child of c1 and
// of c2 starts from the sent entries. SSH_AUTH_SOCK reaches only the children of c1,
// a sent CLAUDE_SSH_ name reaches none, and a variable of the daemon is gone. A
// later adopt replaces the set, and a refused one keeps it (c9125eb8, measured on
// Linux, and on macOS and Windows for the set).
func TestSocketAdoptEnvReachesChildren(t *testing.T) {
	clearClaudeSSHEnv(t)
	clearSessionEnv(t)
	t.Setenv("CLAUDE_SSH_DAEMON_CHILD", "1")
	t.Setenv("CLAUSTRUM_TEST_DAEMON_ONLY", "d1")
	_, sock := newRunningServer(t)
	captureLogBuf(t)
	c1, c2 := dial(t, sock), dial(t, sock)

	before := adoptChildEnv(t, c1, "before", 1)
	if before["CLAUSTRUM_TEST_DAEMON_ONLY"] != "d1" {
		t.Fatalf("before an adopt the child has CLAUSTRUM_TEST_DAEMON_ONLY=%q, want d1", before["CLAUSTRUM_TEST_DAEMON_ONLY"])
	}
	adopt := func(cl *testClient, id int, params string) string {
		return string(cl.call(authed(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"server.adoptEnv","params":%s}`, id, params))))
	}
	if got := adopt(c1, 2, `{"env":["A=1","ZZ_ADOPT=hello","SSH_AUTH_SOCK=/adopted/agent","CLAUDE_SSH_X=1"]}`); !strings.Contains(got, `"ok":true`) {
		t.Fatalf("adopt: %s", got)
	}
	type check struct{ name, want string } // want "" means absent
	expect := func(label string, env map[string]string, checks ...check) {
		t.Helper()
		for _, c := range checks {
			v, ok := env[c.name]
			switch {
			case c.want == "" && ok:
				t.Errorf("%s: the child has %s=%q, want it absent", label, c.name, v)
			case c.want != "" && v != c.want:
				t.Errorf("%s: the child has %s=%q (present %v), want %q", label, c.name, v, ok, c.want)
			}
		}
	}
	expect("c1 after the adopt", adoptChildEnv(t, c1, "c1a", 3),
		check{"A", "1"}, check{"ZZ_ADOPT", "hello"}, check{"SSH_AUTH_SOCK", "/adopted/agent"},
		check{"CLAUDE_SSH_X", ""}, check{"CLAUDE_SSH_DAEMON_CHILD", "1"}, check{"CLAUSTRUM_TEST_DAEMON_ONLY", ""})
	expect("c2 after the adopt", adoptChildEnv(t, c2, "c2a", 1),
		check{"A", "1"}, check{"SSH_AUTH_SOCK", ""}, check{"CLAUSTRUM_TEST_DAEMON_ONLY", ""})

	if got := adopt(c1, 4, `{"env":["B=2"]}`); !strings.Contains(got, `"ok":true`) {
		t.Fatalf("second adopt: %s", got)
	}
	if got := adopt(c1, 5, `{"env":["NOEQUALS"]}`); !strings.Contains(got, `"Invalid params"`) {
		t.Fatalf("refused adopt: %s", got)
	}
	expect("c1 after the second adopt", adoptChildEnv(t, c1, "c1b", 6),
		check{"B", "2"}, check{"A", ""}, check{"SSH_AUTH_SOCK", ""})
}

// TestSocketAdoptEnvNameClasses: the session-bound names that c1 sends reach only
// the children of c1, and a later adopt of c1 replaces them. A connection with no
// adopt keeps those of the daemon. TMPDIR, TMP, TEMP and CLAUDE_CODE_TMPDIR keep the
// daemon value, a kept name keeps the daemon value, and a sent entry beats a kept
// name. On Windows the mixed-case twins follow the same classes (c9125eb8, measured
// on Linux, macOS and Windows).
func TestSocketAdoptEnvNameClasses(t *testing.T) {
	clearClaudeSSHEnv(t)
	clearSessionEnv(t)
	t.Setenv("CLAUDE_SSH_DAEMON_CHILD", "1")
	temps := map[string]string{}
	for _, name := range []string{"TMPDIR", "TMP", "TEMP", "CLAUDE_CODE_TMPDIR"} {
		temps[name] = t.TempDir()
	}
	for name, dir := range temps {
		t.Setenv(name, dir)
	}
	t.Setenv("DISPLAY", ":9")
	t.Setenv("XDG_SESSION_ID", "9")
	t.Setenv("GPG_TTY", "/dev/d")
	t.Setenv("CLAUSTRUM_TEST_KEEP", "daemon")
	_, sock := newRunningServer(t)
	logs := captureLogBuf(t)
	c1, c2 := dial(t, sock), dial(t, sock)

	adopt := func(cl *testClient, id int, params string) {
		t.Helper()
		got := string(cl.call(authed(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"server.adoptEnv","params":%s}`, id, params))))
		if want := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"ok":true}}`, id); got != want {
			t.Fatalf("adopt %d: %s, want %s", id, got, want)
		}
	}
	lastLine := func(want string) {
		t.Helper()
		lines := adoptLines(logs.String())
		if len(lines) == 0 || !strings.HasPrefix(lines[len(lines)-1], want) {
			t.Errorf("last adopt line = %q, want the start %q", lines, want)
		}
	}
	head := "[Server] New processes now start from the environment sent on connection <C>: "
	expect := func(label string, env map[string]string, want map[string]string) {
		t.Helper()
		for name, w := range want {
			v, ok := env[name]
			switch {
			case w == "" && ok:
				t.Errorf("%s: the child has %s=%q, want it absent", label, name, v)
			case w != "" && v != w:
				t.Errorf("%s: the child has %s=%q (present %v), want %q", label, name, v, ok, w)
			}
		}
	}
	session := []string{"DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY", "KRB5CCNAME", "XDG_SEAT", "XDG_VTNR", "GPG_TTY", "WSL_INTEROP",
		"SSH_CONNECTION", "SSH_TTY", "SSH2_X", "XDG_SESSION_ID", "XDG_SESSION_TYPE"}
	sent := []string{"TMPDIR=/s/tmp", "TMP=/s/tmp", "TEMP=/s/tmp", "CLAUDE_CODE_TMPDIR=/s/cct"}
	for _, name := range session {
		sent = append(sent, name+"=/sent/"+strings.ToLower(name))
	}
	sent = append(sent, "XDG_RUNTIME_DIR=/sent/run", "CLAUDE_CODE_OTHER=/sent/other", "ZZ=/sent/zz")
	body, err := json.Marshal(map[string]any{"env": sent})
	if err != nil {
		t.Fatal(err)
	}
	adopt(c1, 1, string(body))
	lastLine(head + "8 variables for every connection, 13 for this connection only.")
	// The session-bound and temp-folder names of the daemon are not in the names.
	if lines := adoptLines(logs.String()); len(lines) > 0 {
		for _, name := range []string{`"DISPLAY"`, `"GPG_TTY"`, `"XDG_SESSION_ID"`, `"TMPDIR"`, `"CLAUDE_CODE_TMPDIR"`} {
			if strings.Contains(lines[len(lines)-1], name) {
				t.Errorf("the first adopt line names %s: %q", name, lines[len(lines)-1])
			}
		}
	}

	shared := map[string]string{"XDG_RUNTIME_DIR": "/sent/run", "CLAUDE_CODE_OTHER": "/sent/other", "ZZ": "/sent/zz"}
	for name, dir := range temps {
		shared[name] = dir
	}
	c1Want := maps.Clone(shared)
	for _, name := range session {
		c1Want[name] = "/sent/" + strings.ToLower(name)
	}
	expect("c1 after the adopt", adoptChildEnv(t, c1, "c1a", 2), c1Want)
	c2Want := maps.Clone(shared)
	for _, name := range session {
		c2Want[name] = ""
	}
	c2Want["DISPLAY"], c2Want["XDG_SESSION_ID"], c2Want["GPG_TTY"] = ":9", "9", "/dev/d"
	expect("c2 after the adopt", adoptChildEnv(t, c2, "c2a", 1), c2Want)

	adopt(c1, 3, `{"env":["ZZ=second","ONLY2=1"]}`)
	lastLine(head + `7 variables for every connection, 0 for this connection only. Names added, removed or changed (at most 40): ["CLAUDE_CODE_OTHER" "ONLY2" "XDG_RUNTIME_DIR" "ZZ"]`)
	c1Want = map[string]string{"ZZ": "second", "ONLY2": "1", "CLAUDE_CODE_OTHER": ""}
	for _, name := range session {
		c1Want[name] = ""
	}
	expect("c1 after the second adopt", adoptChildEnv(t, c1, "c1b", 4), c1Want)
	expect("c2 after the second adopt", adoptChildEnv(t, c2, "c2b", 2),
		map[string]string{"ZZ": "second", "DISPLAY": ":9", "XDG_SESSION_ID": "9", "GPG_TTY": "/dev/d"})

	adopt(c1, 5, `{"env":["Z=1"],"kept":["CLAUSTRUM_TEST_KEEP"]}`)
	expect("a kept name", adoptChildEnv(t, c1, "kept", 6), map[string]string{"Z": "1", "CLAUSTRUM_TEST_KEEP": "daemon"})
	adopt(c1, 7, `{"env":["CLAUSTRUM_TEST_KEEP=sent"],"kept":["CLAUSTRUM_TEST_KEEP"]}`)
	expect("a sent and kept name", adoptChildEnv(t, c1, "keptsent", 8), map[string]string{"CLAUSTRUM_TEST_KEEP": "sent"})
	adopt(c1, 9, `{"env":["Z=1"]}`)
	expect("no kept name", adoptChildEnv(t, c1, "nokept", 10), map[string]string{"CLAUSTRUM_TEST_KEEP": ""})

	if runtime.GOOS != "windows" {
		return
	}
	// On Windows the names are not case-sensitive. adoptChildEnv gives them in
	// upper case.
	adopt(c1, 11, `{"env":["Tmp=/s/twin1","temp=/s/twin2","Zz=1","Display=:7","Gpg_Tty=/s/g","gpg_tty=/s/gl"]}`)
	lastLine(head + "6 variables for every connection, 2 for this connection only.")
	expect("c1 after the twins", adoptChildEnv(t, c1, "c1t", 12),
		map[string]string{"TMP": temps["TMP"], "TEMP": temps["TEMP"], "ZZ": "1", "DISPLAY": ":7", "GPG_TTY": "/s/gl"})
	expect("c2 after the twins", adoptChildEnv(t, c2, "c2t", 3),
		map[string]string{"TMP": temps["TMP"], "TEMP": temps["TEMP"], "ZZ": "1", "DISPLAY": ":9", "GPG_TTY": "/dev/d"})
}

// TestSocketAdoptEnvTakesLoginPATH: after a good read of the login-shell PATH a
// child of an adopted set gets that PATH on Linux and macOS, not the sent one
// (c9125eb8, measured on Linux and macOS). On Windows the sent PATH stays (from
// the code, adoptTakesLoginPATH).
func TestSocketAdoptEnvTakesLoginPATH(t *testing.T) {
	clearClaudeSSHEnv(t)
	f := installPathReadFake(t)
	shellenv.WarmAtStart()
	r := startedRead(t, f)
	f.answer <- pathAnswer{path: "/login/bin"}
	awaitReadEnd(t, r)
	_, sock := newRunningServer(t)
	captureLogBuf(t)
	cl := dial(t, sock)
	adopt := `{"jsonrpc":"2.0","id":1,"method":"server.adoptEnv","params":{"env":["A=1","PATH=/adopted/bin"]}}`
	if got := string(cl.call(authed(adopt))); !strings.Contains(got, `"ok":true`) {
		t.Fatalf("adopt: %s", got)
	}
	want := "/login/bin"
	if runtime.GOOS == "windows" {
		want = "/adopted/bin"
	}
	if got := adoptChildEnv(t, cl, "lp", 2)["PATH"]; got != want {
		t.Errorf("the child of the adopted set has PATH=%q, want %q", got, want)
	}
}

// adoptAfterGoodRead gives the read at the start the PATH /login/1, boots a server
// and adopts params on a new connection. It returns the fake and the connection.
func adoptAfterGoodRead(t *testing.T, params string) (*pathReadFake, *testClient, string) {
	t.Helper()
	clearClaudeSSHEnv(t)
	f := installPathReadFake(t)
	shellenv.WarmAtStart()
	r := startedRead(t, f)
	f.answer <- pathAnswer{path: "/login/1"}
	awaitReadEnd(t, r)
	_, sock := newRunningServer(t)
	captureLogBuf(t)
	cl := dial(t, sock)
	adopt := `{"jsonrpc":"2.0","id":1,"method":"server.adoptEnv","params":` + params + `}`
	if got := string(cl.call(authed(adopt))); !strings.Contains(got, `"ok":true`) {
		t.Fatalf("adopt: %s", got)
	}
	return f, cl, sock
}

// TestSocketAdoptEnvReadsLoginPATHAgain: on Linux against c9125eb8, after a long
// wait the first spawn on a connection that adopted starts one read again. That
// spawn keeps the old PATH, and a later spawn gets the new one. The pause of 10m0s
// is claustrum's own (not measured). Windows reads no login shell.
func TestSocketAdoptEnvReadsLoginPATHAgain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no login shell runs on Windows, so no read again starts")
	}
	f, cl, _ := adoptAfterGoodRead(t, `{"env":["A=1","PATH=/adopted/bin"]}`)
	if got := adoptChildEnv(t, cl, "soon", 2)["PATH"]; got != "/login/1" {
		t.Fatalf("PATH right after the adopt = %q, want /login/1", got)
	}
	noReadStarted(t, f, "a spawn right after the adopt")
	f.advance(11 * time.Minute)
	if got := adoptChildEnv(t, cl, "first", 3)["PATH"]; got != "/login/1" {
		t.Errorf("PATH of the spawn that starts the read again = %q, want /login/1", got)
	}
	r := startedRead(t, f)
	f.answer <- pathAnswer{path: "/login/2"}
	awaitReadEnd(t, r)
	if got := adoptChildEnv(t, cl, "next", 4)["PATH"]; got != "/login/2" {
		t.Errorf("PATH of the next spawn = %q, want /login/2", got)
	}
}

// TestSocketAdoptEnvNoLoginShellReadsNoPATHAgain: on Linux against c9125eb8, a
// connection whose adopt sent "noLoginShell":true starts no read again. That a
// connection with no adopt of its own starts none is claustrum's own, not measured.
func TestSocketAdoptEnvNoLoginShellReadsNoPATHAgain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no login shell runs on Windows, so no read again starts")
	}
	f, cl, sock := adoptAfterGoodRead(t, `{"env":["A=1"],"noLoginShell":true}`)
	f.advance(11 * time.Minute)
	adoptChildEnv(t, cl, "nologin", 2)
	noReadStarted(t, f, "a spawn after an adopt with noLoginShell")
	adoptChildEnv(t, dial(t, sock), "other", 1)
	noReadStarted(t, f, "a spawn on a connection with no adopt")
}

// TestSocketAdoptEnvLaterNoLoginShellEndsTheReadAgain: a later good adopt with
// "noLoginShell":true on the same connection turns the read again off. The adopt
// that comes last decides (claustrum's own, not measured).
func TestSocketAdoptEnvLaterNoLoginShellEndsTheReadAgain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no login shell runs on Windows, so no read again starts")
	}
	f, cl, _ := adoptAfterGoodRead(t, `{"env":["A=1"]}`)
	adopt := `{"jsonrpc":"2.0","id":2,"method":"server.adoptEnv","params":{"env":["A=2"],"noLoginShell":true}}`
	if got := string(cl.call(authed(adopt))); !strings.Contains(got, `"ok":true`) {
		t.Fatalf("second adopt: %s", got)
	}
	f.advance(11 * time.Minute)
	adoptChildEnv(t, cl, "later", 3)
	noReadStarted(t, f, "a spawn after a later adopt with noLoginShell")
}
