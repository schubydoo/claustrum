//go:build unix

package git

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestShadowFolderLinkAndFIFO: a link or a FIFO at the folder of the daemon
// stays, and so does the target of the link (not measured).
func TestShadowFolderLinkAndFIFO(t *testing.T) {
	run := resolveTestRoot(t, t.TempDir())
	h := NewShadowHost(run)
	base := filepath.Join(run, shadowRootName)
	target := t.TempDir()
	mustWrite(t, filepath.Join(target, "keep"), "x")
	link := filepath.Join(base, "link")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	h.dir = link
	h.remakeFolder()
	h.mu.Unlock()
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link: %v, %v, want it kept", fi, err)
	}
	if h.dir != link {
		t.Errorf("the folder of the daemon = %s, want the link %s", h.dir, link)
	}
	if got := entryNames(t, target); len(got) != 1 || got[0] != "keep" {
		t.Errorf("the target of the link holds %v, want keep only", got)
	}
	fifo := filepath.Join(base, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	h.dir = fifo
	h.remakeFolder()
	h.mu.Unlock()
	if err := h.removeOwnFolderFile(fifo); err == nil {
		t.Error("remove of a FIFO: nil, want an error")
	}
	if fi, err := os.Lstat(fifo); err != nil || fi.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("the FIFO: %v, %v, want it kept", fi, err)
	}
	if err := h.removeOwnFolderFile(link); err == nil {
		t.Error("remove of a link: nil, want an error")
	}
	h.Close()
}

// TestUnsearchableDaemonFolderTexts: with mode 0000 4c111e86 answers the
// mkdir of the folder of the repository, and at `.probe` the mkdir of
// `.probe` (measured on Linux).
func TestUnsearchableDaemonFolderTexts(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the root user opens a folder of mode 0000")
	}
	f := newShadowFixture(t)
	f.info(t, f.repo, nil)
	dir := daemonFolder(t, f.run)
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	want := "cannot write its copy of the repository's git config: mkdir " + filepath.Join(dir, shadowRepoName(f.gitDir)) + ": permission denied"
	if raw, _ := f.info(t, f.repo, nil); !strings.Contains(raw, want) {
		t.Errorf("git.info = %s, want %s", raw, want)
	}
	probeWant := "mkdir " + filepath.Join(dir, shadowProbeName) + ": permission denied"
	if err := f.d.Shadow.prepareProbeFolder(dir); err == nil || err.Error() != probeWant {
		t.Errorf("prepareProbeFolder = %v, want %s", err, probeWant)
	}
}

// TestSearchOnlyDaemonFolderProbe: with mode 0100 the call about the
// repository runs with the `.probe` that is there, and git.info succeeds, as
// on 4c111e86 after the kept answer is over (measured on Linux).
func TestSearchOnlyDaemonFolderProbe(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the root user opens a folder of mode 0100")
	}
	f := newShadowFixture(t)
	now := setProbeClock(t)
	f.info(t, f.repo, nil)
	dir := daemonFolder(t, f.run)
	if err := os.Chmod(dir, 0o100); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	*now = now.Add(probeKeep + 1)
	raw, calls := f.info(t, f.repo, nil)
	if !strings.Contains(raw, `"isRepo":true`) {
		t.Errorf("git.info = %s, want a good reply", raw)
	}
	if n := len(probeCalls(calls)); n != 1 {
		t.Errorf("git.info made %d calls about the repository, want 1", n)
	}
}
