//go:build linux

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schubydoo/claustrum/internal/install"
)

// The launcherCheck rows of c9125eb8 on Linux: the supplies spawn, the refusal
// frames with launcherFault, and launcher.resolve with path and launcherCheck. The
// owner and mode facts come from the seam of internal/install: every path is
// root-owned 0755 on a local disk unless a row names it. So the tests run as any
// user, and the temp folder under /tmp passes the walk.

// fakeFacts installs the facts seam: the paths of over get their facts, every other
// path is root-owned 0755. The account that signs in is uid 1000.
func fakeFacts(t *testing.T, over map[string][2]uint32) {
	t.Helper()
	t.Cleanup(install.SetLauncherFactsForTest(func(p string) (uint32, uint32, bool, error) {
		if f, ok := over[p]; ok {
			return f[0], f[1], false, nil
		}
		return 0, 0o755, false, nil
	}, 1000))
}

func sha256Hex(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func faultFrame(msg, fault string) string {
	return `{"jsonrpc":"2.0","id":1,"error":{"code":-32004,"message":"` + msg + `","data":{"launcherFault":"` + fault + `","reason":"launcher"}}}`
}

// TestSpawnSuppliesLauncher pins the supplies spawn (c9125eb8, measured on Linux):
// command "", the launcher gets the launcher args, then the args, and no CLI path.
// With no args it gets no args. The start line names the launcher, "which supplies
// Claude Code". A passing launcherCheck with a command runs <launcher> <command> <args>.
func TestSpawnSuppliesLauncher(t *testing.T) {
	logs := captureLogBuf(t)
	fakeFacts(t, nil)
	wrap, exe := wrapLauncherFixture(t)
	_, sock := newRunningServer(t)
	cl := dial(t, sock)
	// No digest: a race-built test binary can pass the 64 MiB limit of the digest.
	// TestCheckLauncherFileDigest pins the digest test.
	check := map[string]any{}
	echo := map[string]string{"CLAUSTRUM_TEST_HELPER": "echo"}
	ok := `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`

	if got := string(cl.call(launchedSpawnReq(t, map[string]any{"id": "l1", "command": "", "args": []string{"--x", "y"}, "launcher": []string{wrap, "-a"}, "launcherSuppliesCli": true, "launcherCheck": check, "env": echo}))); got != ok {
		t.Fatalf("supplies spawn = %s\nwant %s", got, ok)
	}
	if got := streamBytes(t, cl.waitExit("l1"), "stdout"); got != "-a --x y\n" {
		t.Errorf("launcher argv %q, want %q", got, "-a --x y\n")
	}
	if want := "[process.Manager] Process l1 started, PID="; !strings.Contains(logs.String(), want) || !strings.Contains(logs.String(), ", via launcher "+wrap+", which supplies Claude Code\n") {
		t.Errorf("no supplies start line in:\n%s", logs.String())
	}
	if got := string(cl.call(launchedSpawnReq(t, map[string]any{"id": "l2", "command": "", "launcher": []string{wrap}, "launcherSuppliesCli": true, "launcherCheck": check, "env": echo}))); got != ok {
		t.Fatalf("no-args spawn = %s", got)
	}
	if got := streamBytes(t, cl.waitExit("l2"), "stdout"); got != "\n" {
		t.Errorf("no-args launcher argv %q, want none", got)
	}
	if got := string(cl.call(launchedSpawnReq(t, map[string]any{"id": "l6", "command": exe, "args": []string{"--x"}, "launcher": []string{wrap}, "launcherCheck": check, "env": echo}))); got != ok {
		t.Fatalf("checked spawn with a command = %s", got)
	}
	if got := streamBytes(t, cl.waitExit("l6"), "stdout"); got != exe+" --x\n" {
		t.Errorf("checked launcher argv %q, want %q", got, exe+" --x\n")
	}
}

// TestSpawnEnvOnlyIfSent pins claustrum's own, not measured, rule: a name of
// envOnlyIfSent reaches the child only from the env of the request. The measured part
// is the success of a list (c9125eb8, Linux).
func TestSpawnEnvOnlyIfSent(t *testing.T) {
	fakeFacts(t, nil)
	t.Setenv("T464_ONLY_SENT", "daemon")
	wrap, _ := wrapLauncherFixture(t)
	_, sock := newRunningServer(t)
	cl := dial(t, sock)
	check := map[string]any{}
	env := map[string]string{"CLAUSTRUM_TEST_HELPER": "lookupenv"}
	cl.call(launchedSpawnReq(t, map[string]any{"id": "e1", "command": "", "args": []string{"T464_ONLY_SENT"}, "launcher": []string{wrap}, "launcherSuppliesCli": true, "launcherCheck": check, "envOnlyIfSent": []string{"T464_ONLY_SENT"}, "env": env}))
	if got := streamBytes(t, cl.waitExit("e1"), "stdout"); got != "T464_ONLY_SENT absent\n" {
		t.Errorf("not sent: %q, want the name absent", got)
	}
	env["T464_ONLY_SENT"] = "sent"
	cl.call(launchedSpawnReq(t, map[string]any{"id": "e2", "command": "", "args": []string{"T464_ONLY_SENT"}, "launcher": []string{wrap}, "launcherSuppliesCli": true, "launcherCheck": check, "envOnlyIfSent": []string{"T464_ONLY_SENT"}, "env": env}))
	if got := streamBytes(t, cl.waitExit("e2"), "stdout"); got != "T464_ONLY_SENT set sent\n" {
		t.Errorf("sent: %q, want the sent value", got)
	}
}

// TestSpawnLauncherCheckRefusals pins the -32004 frames of a checked spawn (c9125eb8,
// measured on Linux): the two prefixes, launcherFault before reason, the em dash
// text of an empty launcher, and the digest and owner faults. Each logs its
// failed-start line.
func TestSpawnLauncherCheckRefusals(t *testing.T) {
	logs := captureLogBuf(t)
	s := newTestServer(t)
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	L := mkExec(t, filepath.Join(d, "bin", "launcher.sh"))
	bin := filepath.Join(d, "bin")
	cli := mkExec(t, filepath.Join(d, "cli"))
	writable := "`" + bin + "` can be changed by accounts other than its owner (mode 1777), so the launcher can't be trusted. Remove that write access, or keep the launcher somewhere that has none."
	fakeFacts(t, map[string][2]uint32{bin: {0, 0o1777}})
	cases := []struct {
		row    string
		params map[string]any
		want   string
	}{
		{"supplies writable", map[string]any{"id": "p1", "command": "", "args": []string{"--x"}, "launcher": []string{L}, "launcherSuppliesCli": true, "launcherCheck": map[string]any{}}, faultFrame("the launcher cannot be used: "+writable, "writable")},
		{"command writable", map[string]any{"id": "p1", "command": cli, "args": []string{"--x"}, "launcher": []string{L}, "launcherCheck": map[string]any{"sha256": []string{strings.Repeat("0", 64)}}}, faultFrame("the managed launcher cannot be used: "+writable, "writable")},
		{"supplies empty launcher", map[string]any{"id": "p1", "command": "", "args": []string{"--x"}, "launcher": []string{}, "launcherSuppliesCli": true, "launcherCheck": map[string]any{}}, faultFrame("the launcher cannot be used: the value is set but contains no launcher \xe2\x80\x94 unset it to run without one, or set it to the absolute path of your launcher", "not_absolute")},
		{"relative", map[string]any{"id": "p1", "command": cli, "launcher": []string{"launcher.sh"}, "launcherCheck": map[string]any{}}, faultFrame("the managed launcher cannot be used: the launcher must be an absolute path, not a bare name resolved via PATH", "not_absolute")},
	}
	for _, c := range cases {
		if got := dispatchRaw(t, s, launchedSpawnReq(t, c.params)); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.row, got, c.want)
		}
	}
	fakeFacts(t, map[string][2]uint32{L: {1000, 0o755}})
	own := "`" + L + "` belongs to the account that signs in (uid 1000). Root, or another account named as ownerUid in the policy, must own the launcher and every folder above it."
	if got, want := dispatchRaw(t, s, launchedSpawnReq(t, map[string]any{"id": "p2", "command": cli, "launcher": []string{L}, "launcherCheck": map[string]any{}})), faultFrame("the managed launcher cannot be used: "+own, "owner"); got != want {
		t.Errorf("owner:\n got %s\nwant %s", got, want)
	}
	fakeFacts(t, nil)
	digest := "launcher `" + L + "` matches none of the SHA-256 digests in the policy. Run sha256sum on the host to see its digest."
	if got, want := dispatchRaw(t, s, launchedSpawnReq(t, map[string]any{"id": "p3", "command": cli, "launcher": []string{L}, "launcherCheck": map[string]any{"sha256": []string{strings.Repeat("0", 64)}}})), faultFrame("the managed launcher cannot be used: "+digest, "digest"); got != want {
		t.Errorf("digest:\n got %s\nwant %s", got, want)
	}
	for _, want := range []string{
		"[process.Manager] Failed to start process p1: the launcher cannot be used: " + writable + "\n",
		"[process.Manager] Failed to start process p3: the managed launcher cannot be used: " + digest + "\n",
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("missing log %q in:\n%s", want, logs.String())
		}
	}
}

// resolveCheckFrame sends a launcher.resolve with cliPath, path and launcherCheck.
func resolveCheckFrame(t *testing.T, s *server, path, check string) string {
	t.Helper()
	return dispatchRaw(t, s, authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":{"cliPath":"/x/cli","path":`+jsonText(t, path)+`,"launcherCheck":`+check+`}}`))
}

// TestLauncherResolveCheck pins launcher.resolve with path and launcherCheck
// (c9125eb8, measured on Linux): the usable reply names path with no source, also
// when the policy names another file. The unusable reply has reason, fault and
// hostPolicySeen, and logs one line each time. Without path the reply is the old one
// (claustrum's own, not measured).
func TestLauncherResolveCheck(t *testing.T) {
	logs := captureLogBuf(t)
	fakeFacts(t, nil)
	dir := tempManagedSettingsDir(t)
	s := newTestServer(t)
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	L := mkExec(t, filepath.Join(d, "ok", "launcher.sh"))
	other := mkExec(t, filepath.Join(d, "other", "launcher.sh"))
	base := filepath.Join(dir, managedSettingsBase)
	writeFileMode(t, base, []byte(pwSettings(t, other)), 0o644)
	usable := `{"jsonrpc":"2.0","id":1,"result":{"status":"usable","argv":["` + L + `"],"hostPolicySeen":false}}`
	if got := resolveCheckFrame(t, s, L, `{"sha256":["`+sha256Hex(t, L)+`"]}`); got != usable {
		t.Errorf("right digest:\n got %s\nwant %s", got, usable)
	}
	reason := "launcher `" + L + "` matches none of the SHA-256 digests in the policy. Run sha256sum on the host to see its digest."
	unusable := `{"jsonrpc":"2.0","id":1,"result":{"status":"unusable","reason":"` + reason + `","fault":"digest","hostPolicySeen":false}}`
	for range 2 {
		if got := resolveCheckFrame(t, s, L, `{"sha256":["`+strings.Repeat("0", 64)+`"]}`); got != unusable {
			t.Errorf("wrong digest:\n got %s\nwant %s", got, unusable)
		}
	}
	line := "[LauncherHandler] the launcher that the desktop names is refused: " + reason + "\n"
	if n := strings.Count(logs.String(), line); n != 2 {
		t.Errorf("%d refusal lines, want 2 (one per resolve):\n%s", n, logs.String())
	}
	old := `{"jsonrpc":"2.0","id":1,"result":{"status":"usable","argv":["` + other + `"],"source":"` + base + `"}}`
	if got := dispatchRaw(t, s, authed(`{"jsonrpc":"2.0","id":1,"method":"launcher.resolve","params":{"cliPath":"/x/cli","launcherCheck":{}}}`)); got != old {
		t.Errorf("no path:\n got %s\nwant %s", got, old)
	}
	writeFileMode(t, base, []byte(`{"processWrapper":`+jsonText(t, other)+`,"x":1}`), 0o644)
	seen := `{"jsonrpc":"2.0","id":1,"result":{"status":"usable","argv":["` + L + `"],"hostPolicySeen":true}}`
	if got := resolveCheckFrame(t, s, L, `{}`); got != seen {
		t.Errorf("another key:\n got %s\nwant %s", got, seen)
	}
}
