package git

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/pathcanon"
	"github.com/schubydoo/claustrum/internal/rpc"
	"github.com/schubydoo/claustrum/internal/sysproc"
)

// TestMain runs the test binary as a fixture command or a git stub when
// CLAUSTRUM_TEST_HELPER names a mode, as the TestMain of package main does for
// the modes that the tests of this package use. Otherwise it makes the fixture
// template root and runs the tests.
func TestMain(m *testing.M) {
	// Each helper is a copy of this binary. Under -race the tsan runtime of a
	// child sleeps 1 s before it exits unless GORACE says otherwise. An
	// operator-supplied GORACE still wins.
	if os.Getenv("GORACE") == "" {
		os.Setenv("GORACE", "atexit_sleep_ms=0")
	}

	mode := os.Getenv("CLAUSTRUM_TEST_HELPER")

	// The fork-bomb guard of the TestMain of package main. A copy of this binary
	// that starts with the daemon child marker and no helper mode must never run
	// the suite: a detached copy of the suite that starts copies of itself takes
	// the host down.
	if mode == "" && os.Getenv("CLAUSTRUM_DAEMON_CHILD") == "1" {
		fmt.Fprintln(os.Stderr, "test binary re-exec'd as a daemon child without CLAUSTRUM_TEST_HELPER: refusing to run the suite (a test reached runServe's parent path without the helper seam)")
		os.Exit(1)
	}

	if mode == "" {
		if err := makeFixtureTemplateRoot(); err != nil {
			fmt.Fprintln(os.Stderr, "fixture template root:", err)
			os.Exit(1)
		}
		code := m.Run()
		removeFixtureTemplates()
		os.Exit(code)
	}
	os.Exit(runHelper(mode, os.Args[1:]))
}

// runHelper implements the fixture commands that the tests of this package use,
// as the runHelper of package main does. Each mode's output is fixed and
// newline-exact.
func runHelper(mode string, args []string) int {
	if code, ok := runGitHelper(mode, args); ok {
		return code
	}
	switch mode {
	case "cat": // /bin/cat: copy stdin to stdout until EOF
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "sleep": // /bin/sleep <seconds>
		secs, err := strconv.Atoi(args[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		time.Sleep(time.Duration(secs) * time.Second)
	case "filter-mark":
		// A stand-in git filter. It appends one line to the file args[0], to prove
		// that it ran, and then copies stdin to stdout as cat does.
		appendLine(args[0], "ran")
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "stderr-exit5": // integration fixture: one stderr line, exit 5
		fmt.Fprint(os.Stderr, "err\n")
		return 5
	case "both-streams": // one stdout line, then one stderr line, exit 0
		fmt.Print("out\n")
		fmt.Fprint(os.Stderr, "err\n")
	case "held-stdout":
		// A git call that exits 0 while its child holds the stdout. args[0] gets
		// the pid of this process and of the child, one per line. The child
		// sleeps args[1] seconds. Its stderr is not that of this process.
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		child := exec.Command(exe, args[1])
		child.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "sleep"})
		child.Stdout = os.Stdout
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Print("early\n")
		pids := strconv.Itoa(os.Getpid()) + "\n" + strconv.Itoa(child.Process.Pid) + "\n"
		if err := os.WriteFile(args[0]+".tmp", []byte(pids), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := os.Rename(args[0]+".tmp", args[0]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown CLAUSTRUM_TEST_HELPER mode %q\n", mode)
		return 2
	}
	return 0
}

// appendLine appends line and a newline to the file at path.
func appendLine(path, line string) {
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		_, _ = f.WriteString(line + "\n")
		_ = f.Close()
	}
}

// helperCommand returns this test binary's path plus the env overlay that
// makes a spawned child run it in the given helper mode.
func helperCommand(t *testing.T, mode string) (string, map[string]string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return exe, map[string]string{"CLAUSTRUM_TEST_HELPER": mode}
}

// buildEnv is the environment of a helper child: the environment of this
// process with env merged over it. The buildEnv of package main also drops
// CLAUDE_RPC_TOKEN and applies the login-shell PATH. No test of this package
// sets either one.
func buildEnv(env map[string]string) []string {
	base := os.Environ()
	for k, v := range env {
		base = sysproc.ReplaceOrAppendEnv(base, k, v)
	}
	return base
}

// The test helpers of package main that the tests of this package use, under
// the same names.

const testToken = "s3cret-token"

// The names of package main for the reply types and codes.
type (
	response             = rpc.Response
	worktreeResult       = rpc.WorktreeResult
	worktreeRemoveResult = rpc.WorktreeRemoveResult
	gitInfoResult        = rpc.GitInfoResult
	gitStatusResult      = rpc.GitStatusResult
	branchesResult       = rpc.BranchesResult
)

const (
	codeInvalidParam = rpc.CodeInvalidParam
	codeInternal     = rpc.CodeInternal
)

func canonicalPath(p string) string { return pathcanon.Canonical(p) }

// request is one request line as the tests write it, with the members of the
// request of package main.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Auth    string          `json:"auth"`
}

// bindParams is rpc.Bind for a request of the tests.
func bindParams(req *request, v rpc.PathExpander) *response {
	return rpc.Bind(&rpc.Request{JSONRPC: req.JSONRPC, ID: req.ID, Method: req.Method, Params: req.Params, Auth: req.Auth}, v)
}

// execChildRunDir derives the run dir of the daemon from its socket path, as
// the execChildRunDir of package main does: the folder of the socket path,
// cleaned lexically, and "" only for an empty socket path.
func execChildRunDir(socket string) string {
	if socket == "" {
		return ""
	}
	return filepath.Dir(socket)
}

// rpcLine builds a single authenticated JSON-RPC request line. params is
// marshaled as-is (pass a map or struct); a nil params is sent as `{}` so the
// needParams presence gate is satisfied.
func rpcLine(t *testing.T, method string, params interface{}) string {
	t.Helper()
	if params == nil {
		params = struct{}{}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	req := request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  method,
		Params:  raw,
		Auth:    testToken,
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return string(b)
}

// req builds an authed request line; params key order is irrelevant to the
// server, and json.Marshal escapes paths safely.
func req(id int, method string, params map[string]any) string {
	m := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "auth": testToken}
	if params != nil {
		m["params"] = params
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
}

// gitNoAutoMaintenance turns off the background "git maintenance run --auto
// --detach" that a fixture commit starts. That run creates
// .git/objects/maintenance.lock after git returns, and a TempDir cleanup that
// runs at that moment fails with "directory not empty".
var gitNoAutoMaintenance = []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=maintenance.auto", "GIT_CONFIG_VALUE_0=false"}

// runGit runs a setup git command against dir with a fully isolated identity and
// config, so neither the developer's global gitconfig nor system config can
// perturb the fixture (gpgsign, default branch, hooks, …).
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
	)
	cmd.Env = append(cmd.Env, gitNoAutoMaintenance...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// gitExitOK reports whether `git -C repo <args>` exits 0, for assertions where a
// non-zero exit (e.g. show-ref on a missing ref) is the expected outcome.
func gitExitOK(repo string, args ...string) bool {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	cmd.Env = append(cmd.Env, gitNoAutoMaintenance...)
	return cmd.Run() == nil
}

// syncBuffer is the sink of captureLogBuf. Daemon goroutines can still log into
// the process-global logger while the test reads the buffer, so the read takes
// the same lock as the write.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// captureLogBuf points the default logger at a locked buffer, with no timestamp,
// until the test ends, and returns the buffer. Use it, not a plain bytes.Buffer: see
// syncBuffer.
func captureLogBuf(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldW); log.SetFlags(oldF) })
	return buf
}

// envValue returns the last value of key in env. The match folds case on
// Windows only, as the environment of a child does.
func envValue(env []string, key string) (string, bool) {
	fold := runtime.GOOS == "windows"
	for i := len(env) - 1; i >= 0; i-- {
		k, v, ok := strings.Cut(env[i], "=")
		if !ok {
			continue
		}
		if k == key || (fold && strings.EqualFold(k, key)) {
			return v, true
		}
	}
	return "", false
}

// rpcEnvelope is a minimal reply decoder.
type rpcEnvelope struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeReply(t *testing.T, raw []byte, out any) *rpcEnvelope {
	t.Helper()
	var env rpcEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal reply %s: %v", raw, err)
	}
	if out != nil && env.Result != nil {
		if err := json.Unmarshal(env.Result, out); err != nil {
			t.Fatalf("unmarshal result %s: %v", env.Result, err)
		}
	}
	return &env
}

// homeEnvVar is the variable os.UserHomeDir actually reads on this platform.
// Setting the wrong one leaves the REAL home in place, which would make the
// assertions of TestWorktreeRemoveRefusesHomeDir vacuous. In the unfixed tree
// that test is written to fail against, it would also point a live os.RemoveAll
// at the developer's home directory. That is the accident that
// worktree_homeguard_test.go exists to prevent, so the switch is not a
// portability nicety.
func homeEnvVar() string {
	if runtime.GOOS == "windows" {
		return "USERPROFILE"
	}
	return "HOME"
}

// treeOf lists a directory's contents relative to root, excluding .git, so a
// copied worktree can be compared as a whole rather than file by file.
func treeOf(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil || rel == "." {
			return nil //nolint:nilerr // the root itself is not an entry
		}
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
			if fi.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// isolateGitConfig points the git commands of the daemon at an empty global and
// system config and an empty user ignore file, so a fixture's ignore state is
// decided by its own `.gitignore` alone. It also resets the cache of
// userExcludesFile (resetUserExcludesCache), or the first test that ran a git
// command decides the value for every later test.
func isolateGitConfig(t *testing.T) {
	t.Helper()
	empty := t.TempDir()
	writeFile(t, filepath.Join(empty, "git", "ignore"), "", 0o644)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("XDG_CONFIG_HOME", empty)
	t.Setenv("HOME", empty)
	resetUserExcludesCache(t)
}

func eqTree(t *testing.T, got, want []string, what string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s tree =\n  %v\nwant\n  %v", what, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s tree =\n  %v\nwant\n  %v", what, got, want)
		}
	}
}
