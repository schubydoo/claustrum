//go:build unix

package git

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestOpenRootNoWaitRefusesFIFO pins that a FIFO at the path of a folder of
// the daemon gives the "not a directory" error at once, with no writer. A
// folder still opens, and a plain file keeps the error of os.OpenRoot.
func TestOpenRootNoWaitRefusesFIFO(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	parent, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()

	opens := map[string]func() (*os.Root, error){
		"openRootNoWait":   func() (*os.Root, error) { return openRootNoWait(fifo) },
		"openInRootNoWait": func() (*os.Root, error) { return openInRootNoWait(parent, "fifo") },
	}
	for name, open := range opens {
		done := make(chan error, 1)
		go func() {
			r, err := open()
			if r != nil {
				_ = r.Close()
			}
			done <- err
		}()
		select {
		case err := <-done:
			want := "open " + fifo + ": not a directory"
			if err == nil || err.Error() != want {
				t.Errorf("%s on a FIFO: %v, want %q", name, err, want)
			}
		case <-time.After(10 * time.Second):
			// Free the blocked open with a writer, so the test ends.
			if w, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
				_ = w.Close()
			}
			t.Logf("the open after the writer: %v", <-done)
			t.Errorf("%s waits for a writer at a FIFO", name)
		}
	}

	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if r, err := openRootNoWait(sub); err != nil {
		t.Errorf("openRootNoWait on a folder: %v", err)
	} else {
		_ = r.Close()
	}
	if r, err := openInRootNoWait(parent, "sub"); err != nil {
		t.Errorf("openInRootNoWait on a folder: %v", err)
	} else {
		_ = r.Close()
	}

	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, want := os.OpenRoot(file)
	if _, err := openRootNoWait(file); err == nil || want == nil || err.Error() != want.Error() {
		t.Errorf("openRootNoWait on a plain file: %v, want %v", err, want)
	}
	_, want = parent.OpenRoot("file")
	if _, err := openInRootNoWait(parent, "file"); err == nil || want == nil || err.Error() != want.Error() {
		t.Errorf("openInRootNoWait on a plain file: %v, want %v", err, want)
	}
}

// TestRequestDirAtFIFOAnswersAtOnce pins one call site: a FIFO at the folder of
// the daemon gives requestDir an error at once, with no writer.
func TestRequestDirAtFIFOAnswersAtOnce(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := gitUse{folder: fifo}.requestDir(".s", "claude-test-")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("requestDir at a FIFO: no error")
		}
	case <-time.After(10 * time.Second):
		if w, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			_ = w.Close()
		}
		t.Logf("the open after the writer: %v", <-done)
		t.Error("requestDir waits for a writer at a FIFO")
	}
}

// TestProbeFolderAtFIFOAnswersAtOnce pins the open of the call about the
// repository: a FIFO at the folder of the daemon gives prepareProbeFolder an
// error at once, with no writer.
func TestProbeFolderAtFIFOAnswersAtOnce(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- (&ShadowHost{}).prepareProbeFolder(fifo) }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("prepareProbeFolder at a FIFO: no error")
		}
	case <-time.After(10 * time.Second):
		if w, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			_ = w.Close()
		}
		t.Logf("the open after the writer: %v", <-done)
		t.Error("prepareProbeFolder waits for a writer at a FIFO")
	}
}

// TestStagingFillAtFIFOAnswersAtOnce pins the open of the repository folder in
// the staging fill: a FIFO there gives the "not a directory" error at once,
// with no writer.
func TestStagingFillAtFIFOAnswersAtOnce(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- (&addStage{u: gitUse{repoDir: fifo}}).fill(nil, nil) }()
	select {
	case err := <-done:
		if err == nil || !strings.HasSuffix(err.Error(), "not a directory") {
			t.Errorf("fill at a FIFO: %v, want an error that ends with not a directory", err)
		}
	case <-time.After(10 * time.Second):
		if w, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			_ = w.Close()
		}
		t.Logf("the open after the writer: %v", <-done)
		t.Error("fill waits for a writer at a FIFO")
	}
}
