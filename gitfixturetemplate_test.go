package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Fixture templates. A fixture repository with a fixed shape is built once per
// test binary, and each test copies it. A copy costs no git call, and a git
// call is slow on Windows.
var (
	fixtureTplMu   sync.Mutex
	fixtureTplRoot string
	fixtureTpls    = map[string]string{}
	// fixtureTplBuilder is the test whose build holds fixtureTplMu.
	fixtureTplBuilder atomic.Pointer[testing.T]
)

// A template build runs git with the environment of the process at start,
// before any test sets a variable or puts a stub first on PATH. So the first
// test that builds a template does not change its content.
var (
	tplGit, _  = exec.LookPath("git")
	tplBaseEnv = templateBaseEnv(os.Environ())
)

// tplShapeVars change the repository that git makes. A build takes their
// values from the caller, and the template key holds them. So each set of
// values gets its own template.
var tplShapeVars = []string{"GIT_DEFAULT_HASH", "GIT_DEFAULT_REF_FORMAT", "GIT_TEMPLATE_DIR"}

// templateBaseEnv is env without each GIT_* and CLAUSTRUM_* entry.
func templateBaseEnv(env []string) []string {
	var out []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if u := strings.ToUpper(k); strings.HasPrefix(u, "GIT_") || strings.HasPrefix(u, "CLAUSTRUM_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// tplShape returns the entries of tplShapeVars that the caller has set.
func tplShape() []string {
	var out []string
	for _, k := range tplShapeVars {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// buildGit is runGit for a template build. It runs the git of tplGit with
// tplBaseEnv, the values of tplShapeVars of the caller and the keys of runGit.
func buildGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if tplGit == "" {
		t.Fatal("no git on PATH at start of the test binary")
	}
	cmd := exec.Command(tplGit, append([]string{"-C", dir}, args...)...)
	cmd.Env = append(append(append([]string{}, tplBaseEnv...), tplShape()...),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
	)
	cmd.Env = append(cmd.Env, gitNoAutoMaintenance...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// copyFixtureTemplate copies the template named key into dst, which must not
// exist or must be empty. The first call for key runs build on an empty
// directory to make the template. A build runs git with buildGit only. If git
// acts on an absolute path in the template, such as a remote URL or a gitdir
// file, the caller must rewrite that path in dst.
func copyFixtureTemplate(t *testing.T, key, dst string, build func(t *testing.T, dir string)) {
	t.Helper()
	tpl := fixtureTemplate(t, key, build)
	if err := os.CopyFS(dst, os.DirFS(tpl)); err != nil {
		t.Fatalf("copy fixture template %s: %v", key, err)
	}
}

// copyWorktreeTemplate copies the template named key into dst, as
// copyFixtureTemplate does. The template holds the linked worktrees wts, each
// a path relative to dst. git stores two absolute paths for each worktree: the
// gitdir line of its .git file and the gitdir file of its registration. The
// copy gets both with dst in place of the template folder, in the form that git
// wrote. A build must not call another template, because the lock is held.
func copyWorktreeTemplate(t *testing.T, key, dst string, build func(t *testing.T, dir string), wts ...string) {
	t.Helper()
	tpl := fixtureTemplate(t, key, build)
	if err := os.CopyFS(dst, os.DirFS(tpl)); err != nil {
		t.Fatalf("copy fixture template %s: %v", key, err)
	}
	olds := []string{tpl}
	if r, err := filepath.EvalSymlinks(tpl); err == nil {
		olds = append(olds, r)
	}
	olds = append(olds, resolveTestRoot(t, tpl))
	for _, wt := range wts {
		reg := pointAtCopy(t, filepath.Join(dst, wt, ".git"), "gitdir: ", olds, dst)
		pointAtCopy(t, filepath.Join(filepath.FromSlash(reg), "gitdir"), "", olds, dst)
	}
}

// pointAtCopy rewrites the file that holds lead, one absolute path in the
// template and a newline. The path then starts with dst instead of one of olds.
// It returns the new path.
func pointAtCopy(t *testing.T, file, lead string, olds []string, dst string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := strings.CutPrefix(string(b), lead)
	if !ok || !strings.HasSuffix(p, "\n") {
		t.Fatalf("%s holds %q, want %q, a path and a newline", file, b, lead)
	}
	p = strings.TrimSuffix(p, "\n")
	newRoot := dst
	if !strings.Contains(p, "\\") {
		newRoot = filepath.ToSlash(dst)
	}
	for _, old := range olds {
		if len(p) <= len(old) || (p[len(old)] != '/' && p[len(old)] != '\\') {
			continue
		}
		head, want := filepath.ToSlash(p[:len(old)]), filepath.ToSlash(old)
		if head != want && (runtime.GOOS != "windows" || !strings.EqualFold(head, want)) {
			continue
		}
		p = newRoot + p[len(old):]
		if err := os.WriteFile(file, []byte(lead+p+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Fatalf("%s holds %q, which starts with none of %q", file, b, olds)
	return ""
}

func fixtureTemplate(t *testing.T, key string, build func(t *testing.T, dir string)) string {
	t.Helper()
	if fixtureTplBuilder.Load() == t {
		t.Fatalf("the build of a template asks for the template %s: fixtureTplMu is held", key)
	}
	fixtureTplMu.Lock()
	defer fixtureTplMu.Unlock()
	full := strings.Join(append([]string{key}, tplShape()...), "\x00")
	if tpl, ok := fixtureTpls[full]; ok {
		return tpl
	}
	if fixtureTplRoot == "" {
		t.Fatal("no fixture template root: TestMain did not call makeFixtureTemplateRoot")
	}
	tpl, err := os.MkdirTemp(fixtureTplRoot, key+"-")
	if err != nil {
		t.Fatal(err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(tpl)
		}
	}()
	fixtureTplBuilder.Store(t)
	defer fixtureTplBuilder.Store(nil)
	build(t, tpl)
	fixtureTpls[full] = tpl
	ok = true
	return tpl
}

// makeFixtureTemplateRoot makes the directory that holds the templates.
// TestMain calls it before any test runs, so a test that points TMPDIR at its
// own TempDir cannot move the templates into a directory that a test deletes.
func makeFixtureTemplateRoot() error {
	root, err := os.MkdirTemp("", "claustrum-fixtpl-")
	if err != nil {
		return err
	}
	fixtureTplRoot = root
	return nil
}

// removeFixtureTemplates deletes every template. TestMain calls it after the
// tests ran.
func removeFixtureTemplates() {
	fixtureTplMu.Lock()
	defer fixtureTplMu.Unlock()
	if fixtureTplRoot != "" {
		_ = os.RemoveAll(fixtureTplRoot)
	}
}

// requireTempOutsideCheckout fails the test when the temp dir sits inside a git
// checkout. The create then refuses every worktreeRoot under it, so set TMPDIR
// outside any checkout. It walks the resolved path, as the create does.
func requireTempOutsideCheckout(t *testing.T, dir string) {
	t.Helper()
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			t.Fatalf("temp dir %s is inside a git checkout (%s has a .git entry); set TMPDIR outside it", dir, d)
		}
		if filepath.Dir(d) == d {
			return
		}
	}
}

// TestTemplateBuildIgnoresCallerEnv: a template build does not use GIT_DIR or
// PATH of the test that asks for the template.
func TestTemplateBuildIgnoresCallerEnv(t *testing.T) {
	requireGit(t)
	elsewhere := filepath.Join(t.TempDir(), "g")
	t.Setenv("GIT_DIR", elsewhere)
	t.Setenv("PATH", t.TempDir())
	dst := filepath.Join(t.TempDir(), "r")
	copyFixtureTemplate(t, "caller-env", dst, func(t *testing.T, dir string) {
		buildGit(t, dir, "init", "-q", "-b", "main")
	})
	if _, err := os.Stat(filepath.Join(dst, ".git", "HEAD")); err != nil {
		t.Errorf("the copy has no repository: %v", err)
	}
	if _, err := os.Lstat(elsewhere); err == nil {
		t.Error("the build made the repository at GIT_DIR of the test")
	}
}

// TestTemplateBuildAsksForATemplate: a build that asks for another template
// fails at once. It runs in a copy of the test binary, because the test fails
// there.
func TestTemplateBuildAsksForATemplate(t *testing.T) {
	if os.Getenv("CLAUSTRUM_TPL_NESTED") == "1" {
		copyFixtureTemplate(t, "nested-outer", filepath.Join(t.TempDir(), "o"), func(t *testing.T, dir string) {
			copyFixtureTemplate(t, "nested-inner", filepath.Join(dir, "i"), func(*testing.T, string) {})
		})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTemplateBuildAsksForATemplate$", "-test.timeout=60s")
	cmd.Env = append(os.Environ(), "CLAUSTRUM_TPL_NESTED=1")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "asks for the template nested-inner") {
		t.Fatalf("the nested build: err=%v\n%s", err, out)
	}
}
