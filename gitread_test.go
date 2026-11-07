package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schubydoo/claustrum/internal/git"
	"github.com/schubydoo/claustrum/internal/supervisor"
)

// gitReadReq is a process.spawn request with "gitRead":true and the members of extra.
// A nil value in extra drops that member.
func gitReadReq(t *testing.T, id int, procID, cwd string, args []string, extra map[string]any) string {
	t.Helper()
	params := map[string]any{"id": procID, "command": "git", "args": args, "cwd": cwd, "gitRead": true}
	for k, v := range extra {
		if v == nil {
			delete(params, k)
			continue
		}
		params[k] = v
	}
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "process.spawn", "auth": testToken, "params": params,
	})
	if err != nil {
		t.Fatalf("marshal spawn request: %v", err)
	}
	return string(b)
}

// TestSocketGitReadRefusals pins the refusals of a gitRead spawn (c9125eb8, measured
// on Linux, macOS and Windows): the combine text, the shape text with the count of
// args, and Invalid params for a gitRead that is no bool. No child starts, so the
// cwd is a plain temp folder.
func TestSocketGitReadRefusals(t *testing.T) {
	sock := startSocketServer(t)
	cl := dial(t, sock)
	cwd := t.TempDir()
	top := []string{"rev-parse", "--show-toplevel"}
	const combine = `{"code":-32602,"message":"gitRead needs the command \"git\" and a cwd, and cannot be combined with env, launcher, pty, linkBound, cliTempRoot or sessionFoldersOff","data":{"reason":"other"}}`
	shape := func(n string) string {
		return `{"code":-32602,"message":"not one of the Changes pane's git commands (` + n + ` arguments)","data":{"reason":"other"}}`
	}
	const invalid = `{"code":-32602,"message":"Invalid params"}`
	const terminal = `{"code":-32602,"message":"A terminal needs a key of 32 bytes in hex"}`
	key := strings.Repeat("0", 64)
	cases := []struct {
		name  string
		args  []string
		extra map[string]any
		want  string
	}{
		{"env", top, map[string]any{"env": map[string]string{"FOO": "bar"}}, combine},
		{"no cwd", top, map[string]any{"cwd": nil}, combine},
		{"sleep", []string{"1"}, map[string]any{"command": "sleep"}, combine},
		{"abs git", top, map[string]any{"command": "/usr/bin/git"}, combine},
		{"sessionFoldersOff", top, map[string]any{"sessionFoldersOff": true}, combine},
		{"linkBound object", top, map[string]any{"linkBound": map[string]any{}}, combine},
		{"cliTempRoot", top, map[string]any{"cliTempRoot": true}, combine},
		{"pty with key", top, map[string]any{"pty": map[string]any{"cols": 80, "rows": 24, "key": key}}, combine},
		{"status v2", []string{"status", "--porcelain=v2"}, nil, shape("2")},
		{"commit", []string{"commit", "--allow-empty", "-m", "x"}, nil, shape("4")},
		{"push", []string{"push"}, nil, shape("1")},
		{"no args", []string{}, nil, shape("0")},
		{"status five", []string{"status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all"}, nil, shape("5")},
		{"empty env passes", []string{"status"}, map[string]any{"env": map[string]string{}}, shape("1")},
		{"gitRead object", top, map[string]any{"gitRead": map[string]any{"seconds": 5}}, invalid},
		{"gitRead string", top, map[string]any{"gitRead": "true"}, invalid},
		{"gitRead number", top, map[string]any{"gitRead": 1}, invalid},
		{"gitRead array", top, map[string]any{"gitRead": []bool{true}}, invalid},
		{"pty true", top, map[string]any{"pty": true}, invalid},
		{"cliTempRoot text", top, map[string]any{"cliTempRoot": cwd}, invalid},
		{"cliTempRoot object", top, map[string]any{"cliTempRoot": map[string]any{}}, invalid},
		{"pty object no key", top, map[string]any{"pty": map[string]any{"cols": 80, "rows": 24}}, terminal},
		{"pty empty object", top, map[string]any{"pty": map[string]any{}}, terminal},
		{"launcher array", top, map[string]any{"launcher": []string{"/x"}}, combine},
	}
	var frames []json.RawMessage
	for i, c := range cases {
		got := string(cl.call(gitReadReq(t, i+1, "gr", cwd, c.args, c.extra)))
		want := `{"jsonrpc":"2.0","id":` + itoa(i+1) + `,"error":` + c.want + `}`
		if got != want {
			t.Errorf("%s: reply = %s\nwant %s", c.name, got, want)
		}
		frames = append(frames, json.RawMessage(got))
	}
	assertGolden(t, "socket_process_gitread_refusals.golden.json", encodeGolden(t, frames))
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// TestSocketGitReadRuns runs allowed shapes of the pane's git in a repository and in
// a folder with no repository. c9125eb8 answers success in both, and git exits 128
// in the folder with no repository (measured on Linux and Windows). gitRead false
// and null give a plain spawn (measured on Linux).
func TestSocketGitReadRuns(t *testing.T) {
	requireGit(t)
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	writeFile(t, filepath.Join(repo, "a.txt"), "hi\n", 0o644)
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-q", "-m", "init")
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatal(err)
	}
	sock := startSocketServer(t)
	cl := dial(t, sock)
	top := []string{"rev-parse", "--show-toplevel"}
	ok := `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`
	if got := string(cl.call(gitReadReq(t, 1, "g1", repo, top, nil))); got != ok {
		t.Fatalf("spawn = %s\nwant %s", got, ok)
	}
	if got := streamOf(t, cl.waitExit("g1"), "stdout"); got != string(out) && resolved(t, got) != resolved(t, string(out)) {
		t.Errorf("stdout of the pane's git = %q, want %q", got, out)
	}
	if got := string(cl.call(gitReadReq(t, 2, "g2", repo, []string{"status", "--porcelain=v2", "--branch", "-z"}, nil))); !strings.Contains(got, `"success":true`) {
		t.Fatalf("status spawn = %s", got)
	}
	if got := streamOf(t, cl.waitExit("g2"), "stdout"); !strings.Contains(got, "# branch.oid ") || !strings.HasSuffix(got, "\x00") {
		t.Errorf("stdout of status = %q, want the branch lines ending in NUL", got)
	}
	// The -c items reach the child: core.quotepath=false prints the name as it is.
	writeFile(t, filepath.Join(repo, "\u00e9.txt"), "new\n", 0o644)
	others := []string{"ls-files", "--others", "--exclude-standard", "--full-name", ":/"}
	want, err := exec.Command("git", append([]string{"-C", repo, "-c", "core.quotepath=false"}, others...)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(cl.call(gitReadReq(t, 6, "g6", repo, others, nil))); !strings.Contains(got, `"success":true`) {
		t.Fatalf("ls-files spawn = %s", got)
	}
	if got := streamOf(t, cl.waitExit("g6"), "stdout"); got != string(want) || strings.Contains(got, `\`) {
		t.Errorf("stdout of ls-files = %q, want %q", got, want)
	}
	// The env reaches the child: GIT_DIR=<null device> names the folder in the
	// text of git (measured on Linux and Windows).
	if got := string(cl.call(gitReadReq(t, 3, "g3", t.TempDir(), top, nil))); !strings.Contains(got, `"success":true`) {
		t.Fatalf("spawn in a folder with no repository = %s", got)
	}
	fs := cl.waitExit("g3")
	if fs[len(fs)-1].ExitCode == nil || *fs[len(fs)-1].ExitCode != 128 {
		t.Errorf("exit frame in a folder with no repository = %+v, want code 128", fs[len(fs)-1])
	}
	if got, want := streamOf(t, fs, "stderr"), "fatal: not a git repository: '"+os.DevNull+"'\n"; got != want {
		t.Errorf("stderr in a folder with no repository = %q, want %q", got, want)
	}
	for i, v := range []any{false, nil} {
		extra := map[string]any{"gitRead": v, "args": []string{"status", "-s"}}
		if v == nil {
			extra["gitRead"] = json.RawMessage("null")
		}
		id := 4 + i
		if got := string(cl.call(gitReadReq(t, id, "p"+itoa(id), repo, nil, extra))); !strings.Contains(got, `"success":true`) {
			t.Errorf("gitRead %v with a refused shape = %s, want a plain spawn", v, got)
		}
		// Wait for the child: on Windows a running git holds the repo folder open
		// and the cleanup of the temp folder fails.
		cl.waitExit("p" + itoa(id))
	}
}

// resolved is the path that git printed in out, with its links resolved.
func resolved(t *testing.T, out string) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(strings.TrimSpace(out))
	if err != nil {
		t.Fatalf("resolve %q: %v", out, err)
	}
	return p
}

// streamOf is the bytes of the frames of stream in fs.
func streamOf(t *testing.T, fs []supervisor.StreamFrame, stream string) string {
	t.Helper()
	var b strings.Builder
	for _, f := range fs {
		if f.Stream != stream {
			continue
		}
		d, err := base64.StdEncoding.DecodeString(f.Data)
		if err != nil {
			t.Fatalf("frame data %q: %v", f.Data, err)
		}
		b.Write(d)
	}
	return b.String()
}

// TestGitReadCwdBeforeShape pins the order of c9125eb8: the cwd test comes before
// the shape test. A missing cwd with a refused shape gets the cwd frame with
// cwd_missing (measured on Linux, macOS and Windows). A refused shape in a cwd that exists
// gets the shape frame and its Failed to start line (measured on Linux).
func TestGitReadCwdBeforeShape(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing-dir")
	afile := filepath.Join(dir, "README")
	writeFile(t, afile, "x\n", 0o644)
	logs := captureLogBuf(t)
	spawn := func(id, cwd string, args []string) string {
		return spawnErrorOf(t, map[string]any{"id": id, "command": "git", "args": args, "cwd": cwd, "gitRead": true})
	}
	refused := []string{"status", "--porcelain=v2"}
	top := []string{"rev-parse", "--show-toplevel"}
	cwdFrame := errJSON(t, -32603, "chdir "+missing+": "+statErrText(t, missing), "cwd_missing")
	for _, c := range []struct {
		name, id, cwd string
		args          []string
		want          string
	}{
		{"missing cwd, refused shape", "gr-c1", missing, refused, cwdFrame},
		{"missing cwd, allowed shape", "gr-c2", missing, top, cwdFrame},
		{"cwd is a file", "gr-c6", afile, top, errJSON(t, -32603, "chdir "+afile+": not a directory", "cwd_missing")},
		{"refused shape", "gr-c5", dir, refused, `{"code":-32602,"message":"not one of the Changes pane's git commands (2 arguments)","data":{"reason":"other"}}`},
	} {
		if got := spawn(c.id, c.cwd, c.args); got != c.want {
			t.Errorf("%s: error = %s\nwant    %s", c.name, got, c.want)
		}
	}
	if line := "[process.Manager] Failed to start process gr-c5: not one of the Changes pane's git commands (2 arguments)"; !strings.Contains(logs.String(), line) {
		t.Errorf("log lacks %q", line)
	}
}

// TestGitReadNoSettingsCopyFrame pins the frame of a gitRead spawn in a repository
// in the state fallback-git, as c9125eb8 sends it (measured on Linux, macOS
// and Windows).
// The step error reaches the reply wrapped, as the start returns it.
func TestGitReadNoSettingsCopyFrame(t *testing.T) {
	err := &supervisor.GitReadStepError{Err: gitReadStepError(git.ErrNoSettingsCopy)}
	b, merr := json.Marshal(spawnErrorReply(1, err, ""))
	if merr != nil {
		t.Fatal(merr)
	}
	const want = `{"jsonrpc":"2.0","id":1,"error":{"code":-32008,"message":"the helper cannot give git its own copy of a repository's settings on this host (this git does not support it, or the app switched it off)","data":{"reason":"other"}}}`
	if string(b) != want {
		t.Errorf("reply = %s\nwant    %s", b, want)
	}
	other := &supervisor.GitReadStepError{Err: gitReadStepError(errors.New("x"))}
	if b, _ := json.Marshal(spawnErrorReply(2, other, "")); string(b) != `{"jsonrpc":"2.0","id":2,"error":{"code":-32603,"message":"x","data":{"reason":"other"}}}` {
		t.Errorf("another step error = %s, want -32603", b)
	}
}
