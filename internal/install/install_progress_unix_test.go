//go:build unix

package install

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// On Linux and macOS the final progress line comes right after the checksum
// passes (finalProgressAfterRename is false). So a step after the checksum
// that fails, here the chmod of the staging file, still leaves both lines.
// The rule after a passed checksum is from the code, not measured.
func TestInstallFinalProgressFollowsTheChecksumOffWindows(t *testing.T) {
	old := chmodStaged
	chmodStaged = func(string, os.FileMode) error { return errors.New("chmod refused") }
	t.Cleanup(func() { chmodStaged = old })
	lastInstallFetch, lastInstallFinal = nil, nil
	t.Cleanup(func() { lastInstallFetch, lastInstallFinal = nil, nil })
	zst := fakeCLIZst(t, 0)
	sum := sha256.Sum256(zst)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(zst)))
		_, _ = w.Write(zst)
	}))
	defer srv.Close()
	n := len(zst)
	want := fmt.Sprintf("__INSTALL_PROGRESS__{\"phase\":\"download\",\"bytes\":0,\"total\":%d}\n"+
		"__INSTALL_PROGRESS__{\"phase\":\"download\",\"bytes\":%d,\"total\":%d}", n, n, n)
	cliPath := filepath.Join(t.TempDir(), "1.0.0")
	stage := func(d string) (string, error) {
		return stageDownload(srv.URL+"/cli.zst", hex.EncodeToString(sum[:]), d)
	}
	var err error
	out := captureStdout(t, func() { err = stageAndInstall(cliPath, stage, "") })
	if err == nil || err.Error() != "chmod refused" {
		t.Fatalf("stageAndInstall = %v, want the chmod error", err)
	}
	var got []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "__INSTALL_PROGRESS__") {
			got = append(got, l)
		}
	}
	if strings.Join(got, "\n") != want {
		t.Fatalf("progress lines =\n%s\nwant\n%s", strings.Join(got, "\n"), want)
	}
}
