package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// launcherShapeReq is a process.spawn request with the members of params and id n.
func launcherShapeReq(t *testing.T, n int, params map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": n, "method": "process.spawn", "auth": testToken, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestSocketLauncherShapeRefusals pins the shape test of the launcher members of
// c9125eb8 (measured on Linux, macOS and Windows): the combined text with the reason
// other, before "Command is required", and Invalid params for a member of the wrong
// type. None of these starts a child or tests a file, so the frames hold on every OS.
// Each shape refusal logs its failed-start line (measured on Linux).
func TestSocketLauncherShapeRefusals(t *testing.T) {
	logs := captureLogBuf(t)
	sock := startSocketServer(t)
	cl := dial(t, sock)
	const shape = `{"code":-32602,"message":"invalid process.spawn: launcherCheck needs a launcher; launcherSuppliesCli needs a launcherCheck and an empty command; envOnlyIfSent needs launcherSuppliesCli","data":{"reason":"other"}}`
	const invalid = `{"code":-32602,"message":"Invalid params"}`
	cmd, L := "/opt/x/cli", "/opt/x/launcher.sh"
	digest := map[string]any{"sha256": []string{strings.Repeat("0", 64)}}
	cases := []struct {
		name   string
		params map[string]any
		want   string
	}{
		{"check only", map[string]any{"command": cmd, "launcherCheck": digest}, shape},
		{"check {} only", map[string]any{"command": cmd, "launcherCheck": map[string]any{}}, shape},
		{"supplies with command", map[string]any{"command": cmd, "launcher": []string{L}, "launcherSuppliesCli": true, "launcherCheck": map[string]any{}}, shape},
		{"supplies no check", map[string]any{"command": "", "launcher": []string{L}, "launcherSuppliesCli": true}, shape},
		{"supplies no launcher", map[string]any{"command": "", "launcherSuppliesCli": true, "launcherCheck": map[string]any{}}, shape},
		{"envOnlyIfSent alone", map[string]any{"command": cmd, "envOnlyIfSent": []string{"FOO"}}, shape},
		{"envOnlyIfSent no supplies", map[string]any{"command": cmd, "launcher": []string{L}, "launcherCheck": digest, "envOnlyIfSent": []string{"FOO"}}, shape},
		{"all three, no launcher", map[string]any{"command": "", "launcherSuppliesCli": true, "launcherCheck": digest, "envOnlyIfSent": []string{"FOO"}}, shape},
		{"envOnlyIfSent true", map[string]any{"command": "", "launcher": []string{L}, "launcherSuppliesCli": true, "launcherCheck": digest, "envOnlyIfSent": true}, invalid},
		{"envOnlyIfSent string", map[string]any{"command": "", "launcher": []string{L}, "launcherSuppliesCli": true, "launcherCheck": digest, "envOnlyIfSent": "FOO"}, invalid},
		{"envOnlyIfSent object", map[string]any{"command": cmd, "envOnlyIfSent": map[string]any{}}, invalid},
		{"sha256 string", map[string]any{"command": cmd, "launcher": []string{L}, "launcherCheck": map[string]any{"sha256": "x"}}, invalid},
		{"supplies string", map[string]any{"command": cmd, "launcherSuppliesCli": "true"}, invalid},
	}
	var frames []json.RawMessage
	for i, c := range cases {
		c.params["id"] = "l" + itoa(i+1)
		got := string(cl.call(launcherShapeReq(t, i+1, c.params)))
		want := `{"jsonrpc":"2.0","id":` + itoa(i+1) + `,"error":` + c.want + `}`
		if got != want {
			t.Errorf("%s: reply = %s\nwant %s", c.name, got, want)
		}
		frames = append(frames, json.RawMessage(got))
	}
	assertGolden(t, "socket_process_launcher_shape.golden.json", encodeGolden(t, frames))
	out := logs.String()
	if want := "[process.Manager] Failed to start process l4: invalid process.spawn: launcherCheck needs a launcher; launcherSuppliesCli needs a launcherCheck and an empty command; envOnlyIfSent needs launcherSuppliesCli\n"; !strings.Contains(out, want) {
		t.Errorf("no shape line %q in:\n%s", want, out)
	}
	if strings.Contains(out, "Failed to start process l9:") {
		t.Errorf("an Invalid params refusal must write no failed-start line:\n%s", out)
	}
}

// TestLauncherResolvePathNeedsCheck pins the launcher.resolve refusal of a path with
// no launcherCheck (c9125eb8, measured on Linux), with no data member. The cliPath
// test comes first. No settings file is read, so it holds on every OS.
func TestLauncherResolvePathNeedsCheck(t *testing.T) {
	s := newTestServer(t)
	cases := []struct{ line, want string }{
		{authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":{"cliPath":"/x/cli","path":"/x/launcher.sh"}}`), `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"launcherCheck is required with path"}}`},
		{authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":{"path":"/x/launcher.sh","launcherCheck":{}}}`), `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"cliPath is required"}}`},
	}
	for _, c := range cases {
		if got := dispatchRaw(t, s, c.line); got != c.want {
			t.Errorf("%s\n got %s\nwant %s", c.line, got, c.want)
		}
	}
}

// TestCapabilitiesListSuppliesCli pins the place of launcher.supplies_cli: right
// after launcher.non_string_refused, on Linux, macOS and Windows (c9125eb8, measured).
func TestCapabilitiesListSuppliesCli(t *testing.T) {
	s := newTestServer(t)
	list := s.featuresFor()
	for i, f := range list {
		if f == "launcher.non_string_refused" {
			if i+1 >= len(list) || list[i+1] != "launcher.supplies_cli" {
				t.Fatalf("launcher.supplies_cli does not follow launcher.non_string_refused: %v", list)
			}
			return
		}
	}
	t.Fatalf("no launcher.non_string_refused in %v", list)
}

// TestSpawnNoLoginShellAccepted pins that a noLoginShell member changes nothing in
// the reply: the spawn succeeds and the child runs (c9125eb8, measured on Linux and
// Windows).
func TestSpawnNoLoginShellAccepted(t *testing.T) {
	_, sock := newRunningServer(t)
	cl := dial(t, sock)
	echo, env := helperCommand(t, "echo")
	got := string(cl.call(launcherShapeReq(t, 1, map[string]any{"id": "n1", "command": echo, "args": []string{"hi"}, "env": env, "noLoginShell": true})))
	if want := `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`; got != want {
		t.Fatalf("reply = %s\nwant %s", got, want)
	}
	if out := streamBytes(t, cl.waitExit("n1"), "stdout"); out != "hi\n" {
		t.Errorf("stdout %q, want %q", out, "hi\n")
	}
}
