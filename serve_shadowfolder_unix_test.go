//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schubydoo/claustrum/internal/git"
	"github.com/schubydoo/claustrum/internal/wirelog"
)

// plainServeRepo makes a repository with one empty commit with the real git
// and returns its folder. It clears the git variables of the test's
// environment, so that only that repository counts.
func plainServeRepo(t *testing.T) string {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_OBJECT_DIRECTORY", "GIT_INDEX_FILE",
		"GIT_CONFIG", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS", "LC_ALL", "LANGUAGE"} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	repo := filepath.Join(resolveTestRoot(t, t.TempDir()), "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command(gitBin, args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	return repo
}

// TestServeDaemonRemovesItsShadowFolder pins the wiring of the -serve daemon:
// its settings control has the folder of the socket as its run folder, and the
// teardown removes the folder of the daemon and leaves the empty git-shadow.
// 5fd08069 does so at -stop and at server.shutdown (measured on Linux).
func TestServeDaemonRemovesItsShadowFolder(t *testing.T) {
	repo := plainServeRepo(t)
	run := resolveTestRoot(t, shortTempDir(t))
	sock := filepath.Join(run, "s.sock")
	s, err := newServerOnSocket(sock, "tok", "", wirelog.Options{}, false, false)
	if err != nil {
		t.Fatalf("newServerOnSocket: %v", err)
	}
	t.Cleanup(func() { s.procs.KillAll() })
	t.Cleanup(func() { _ = s.ln.Close() })
	line := strings.Replace(rpcLine(t, "git.info", map[string]any{"path": repo}), testToken, "tok", 1)
	if raw := dispatchRaw(t, s, line); !strings.Contains(raw, `"settingsControl":"shadow"`) {
		t.Fatalf("git.info = %s", raw)
	}
	root := git.ShadowFolderForTest(run)
	names := shadowRootNames(t, root)
	if len(names) != 1 {
		t.Fatalf("git-shadow holds %v, want one folder", names)
	}
	dir := filepath.Join(root, names[0])
	s.signalShutdown()
	s.closeAll(sock)
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Errorf("the folder of the daemon after the teardown: %v, want it gone", err)
	}
	fi, err := os.Lstat(root)
	if err != nil || fi.Mode() != os.ModeDir|0o700 {
		t.Fatalf("git-shadow after the teardown: %v, %v, want an empty folder with 0700", fi, err)
	}
	if names := shadowRootNames(t, root); len(names) != 0 {
		t.Errorf("git-shadow after the teardown holds %v", names)
	}
}

// shadowRootNames are the names in dir.
func shadowRootNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}
