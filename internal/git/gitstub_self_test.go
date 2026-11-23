package git

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A second fixture finds the stub of the first one on PATH. It keeps the true git,
// and a git call through the stub still answers.
func TestTwoStubFixturesKeepRealGit(t *testing.T) {
	newShadowFixture(t)
	want := os.Getenv("CLAUSTRUM_GITSTUB_REAL")
	onPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	stub, err := exec.LookPath(filepath.Join(gitSlowStubDir(t), "git"))
	if err != nil {
		t.Fatal(err)
	}
	if !sameExeFile(onPath, stub) {
		t.Fatalf("git on PATH is %q, want the stub %q", onPath, stub)
	}
	newShadowFixture(t)
	if got := os.Getenv("CLAUSTRUM_GITSTUB_REAL"); got != want {
		t.Fatalf("CLAUSTRUM_GITSTUB_REAL = %q after the second fixture, want %q", got, want)
	}
	out, err := exec.Command("git", "--version").CombinedOutput()
	if err != nil || !strings.HasPrefix(string(out), "git version") {
		t.Fatalf("git --version through the stub = %q, %v", out, err)
	}
}

// Each place of the stub that starts the real git refuses the stub itself. If a
// place loses the refusal, it starts this binary with no helper mode and a filter
// that matches no test, which exits 0 and starts no further copy.
func TestGitStubRefusesItselfAtEachCall(t *testing.T) {
	installGitSlowStub(t, realGitPath(t))
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUSTRUM_GITSTUB_REAL", self)
	t.Setenv("CLAUSTRUM_TEST_HELPER", "")
	noTests := []string{"-test.run=^$"}
	if code := runGitSlow(noTests); code != 127 {
		t.Errorf("runGitSlow = %d, want 127", code)
	}
	if code := runGitReal(noTests); code != 127 {
		t.Errorf("runGitReal = %d, want 127", code)
	}
	err = applyGitStubStep(append([]string{"git"}, noTests...))
	if err == nil || !strings.Contains(err.Error(), "is the stub itself") {
		t.Errorf("the git step = %v, want the refusal", err)
	}
}

// The installed stub matches itself by file identity, under each name. The check
// mode starts no program, so a lost refusal cannot start a chain.
func TestBuiltGitStubRefusesItself(t *testing.T) {
	realGit := realGitPath(t)
	stub, err := exec.LookPath(filepath.Join(gitSlowStubDir(t), "git"))
	if err != nil {
		t.Fatal(err)
	}
	check := func(name string) (int, string) {
		t.Helper()
		cmd := exec.Command(stub)
		cmd.Env = buildEnv(map[string]string{"CLAUSTRUM_TEST_HELPER": "git-real-check", "CLAUSTRUM_GITSTUB_REAL": name})
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		var ee *exec.ExitError
		if err != nil && !errors.As(err, &ee) {
			t.Fatal(err)
		}
		return cmd.ProcessState.ExitCode(), stderr.String()
	}
	names := []string{stub}
	link := filepath.Join(t.TempDir(), filepath.Base(stub))
	if err := os.Symlink(stub, link); err == nil {
		names = append(names, link)
	} else if runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		names = append(names, strings.ToUpper(stub))
	}
	for _, name := range names {
		if code, stderr := check(name); code != 127 || !strings.Contains(stderr, "is the stub itself") {
			t.Errorf("REAL %q: exit %d, stderr %q, want 127 and the refusal", name, code, stderr)
		}
	}
	if code, stderr := check(realGit); code != 0 {
		t.Errorf("REAL %q (the true git): exit %d, stderr %q, want 0", realGit, code, stderr)
	}
}
