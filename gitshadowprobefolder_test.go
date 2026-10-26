package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// probeTestHost returns a settings control with a run folder and the folder of
// a daemon in its `git-shadow`.
func probeTestHost(t *testing.T) (*shadowHost, string) {
	t.Helper()
	run := t.TempDir()
	h := newShadowHost(run)
	dir := filepath.Join(h.runDir, shadowRootName, "own")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return h, dir
}

// TestProbeFolderRemovesLeftovers: the preparation of `.probe` deletes each
// listed entry as a tree, and then a rebase folder that is empty. A planted
// `keepme` stays, and so does a rebase folder that still holds an entry.
func TestProbeFolderRemovesLeftovers(t *testing.T) {
	h, dir := probeTestHost(t)
	probe := filepath.Join(dir, shadowProbeName)
	for _, name := range probeLeftovers {
		if name == "worktrees" || name == "reftable" {
			name += "/a-file"
		}
		mustWriteAll(t, filepath.Join(probe, filepath.FromSlash(name)))
	}
	for _, name := range []string{"keepme", "rebase-apply/other"} {
		mustWriteAll(t, filepath.Join(probe, filepath.FromSlash(name)))
	}
	if err := h.prepareProbeFolder(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range append(append([]string{}, probeLeftovers...), "rebase-merge") {
		if _, err := os.Lstat(filepath.Join(probe, filepath.FromSlash(name))); !os.IsNotExist(err) {
			t.Errorf(".probe/%s is still there (%v)", name, err)
		}
	}
	for _, name := range []string{"keepme", "rebase-apply/other"} {
		if _, err := os.Lstat(filepath.Join(probe, filepath.FromSlash(name))); err != nil {
			t.Errorf(".probe/%s went: %v", name, err)
		}
	}
}

// TestProbeFolderFiles: a folder at HEAD becomes the file of 25 bytes, as on
// 5fd08069 (measured on Linux). A HEAD with another text gets the text again
// (claustrum's own, not measured). A config that holds its text keeps its inode and its time, as
// on 5fd08069 (measured on Linux).
func TestProbeFolderFiles(t *testing.T) {
	head := func(t *testing.T, plant func(head string)) {
		t.Helper()
		h, dir := probeTestHost(t)
		path := filepath.Join(dir, shadowProbeName, "HEAD")
		plant(path)
		if err := h.prepareProbeFolder(dir); err != nil {
			t.Fatal(err)
		}
		if got := mustRead(t, path); got != shadowTestFirstHead || len(got) != 25 {
			t.Errorf("HEAD = %q, want %q", got, shadowTestFirstHead)
		}
	}
	t.Run("folder at HEAD", func(t *testing.T) {
		head(t, func(path string) { mustWriteAll(t, filepath.Join(path, "x")) })
	})
	t.Run("changed HEAD", func(t *testing.T) {
		head(t, func(path string) {
			mustWriteAll(t, path)
			if err := os.WriteFile(path, []byte("ref: refs/heads/main\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("unchanged config", func(t *testing.T) {
		h, dir := probeTestHost(t)
		config := filepath.Join(dir, shadowProbeName, "config")
		mustWriteAll(t, config)
		if err := os.WriteFile(config, []byte(probeConfigText), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-time.Hour).Truncate(time.Second)
		if err := os.Chtimes(config, old, old); err != nil {
			t.Fatal(err)
		}
		before, err := os.Stat(config)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.prepareProbeFolder(dir); err != nil {
			t.Fatal(err)
		}
		after, err := os.Stat(config)
		if err != nil || !os.SameFile(before, after) || !after.ModTime().Equal(old) {
			t.Errorf("config after the preparation: %v, want the same file with its time %v", err, old)
		}
	})
}

// TestProbeFolderRefusal: a `.probe` with no HEAD in a folder that takes no new
// entry gets the refusal, and no call about the repository runs. On 5fd08069 a
// git.info in that state answers -32603 with this text and runs no git
// (measured on Linux).
func TestProbeFolderRefusal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a folder mode does not deny a new file on Windows")
	}
	if os.Getuid() == 0 {
		t.Skip("the root user makes an entry in a folder of mode 0500")
	}
	f := newShadowFixture(t)
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, `"isRepo":true`) {
		t.Fatalf("git.info = %s", raw)
	}
	now := setProbeClock(t)
	*now = now.Add(probeKeep)
	probe := filepath.Join(daemonFolder(t, f.run), shadowProbeName)
	if err := os.Remove(filepath.Join(probe, "HEAD")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(probe, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(probe, 0o700) })
	raw, calls := f.info(t, f.repo, nil)
	q := regexp.QuoteMeta(strings.Trim(jsonQuoted(t, probe), `"`))
	want := regexp.MustCompile(`^\{"jsonrpc":"2.0","id":1,"error":\{"code":-32603,"message":"Claude could not prepare its own files on the remote machine: cannot write \\"` +
		q + `\\": open ` + q + `/\.tmp-[0-9]+: permission denied"\}\}$`)
	if !want.MatchString(raw) {
		t.Errorf("git.info = %s\nwant %s", raw, want)
	}
	if n := len(probeCalls(calls)); n != 0 {
		t.Errorf("git.info made %d calls about the repository, want none", n)
	}
	if n := len(configReads(calls)); n != 0 {
		t.Errorf("git.info made %d config reads, want none", n)
	}
}

// mustWriteAll makes the folders of path and an empty file at path.
func mustWriteAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}
