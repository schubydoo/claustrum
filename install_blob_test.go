package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The -cli-zst blob goes right after the new CLI is in place, before the CLI
// runs --version. On 5fd08069 the stub sees no blob at its run (cells L11, M4
// and W7, Linux, macOS and Windows VMs). The blob lies in the cli folder here,
// so the folder list of the stub shows it.
func TestInstallBlobIsGoneWhenTheNewCLIRuns(t *testing.T) {
	f := newStubFixture(t)
	blob := filepath.Join(f.cliDir, "in.zst")
	if err := os.WriteFile(blob, stubZst(t), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := captureInstallFacts(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9", cliZst: blob}); got.CliError != "" {
		t.Fatalf("cliError = %q", got.CliError)
	}
	if dir := mustLogLine(t, f, "DIR "); dir != "DIR "+filepath.Base(f.cli) {
		t.Errorf("the folder at the --version run: %q, want the new CLI only", dir)
	}
	f.wantNames(t, "L11", filepath.Base(f.cli))
}

// A -cli-zst blob in the cli folder with a name of the sweep stays after a
// failed install. On 5fd08069 a blob C/1.0.0.zst that is 20 minutes old and no
// zstd archive stays, with the decoder text (cell L3, Linux VM).
func TestInstallKeepsABadBlobWithASweepName(t *testing.T) {
	f := newStubFixture(t)
	blob := filepath.Join(f.cliDir, "1.0.0.zst")
	if err := os.WriteFile(blob, []byte(strings.Repeat("not zstd ", 30)), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-20 * time.Minute)
	if err := os.Chtimes(blob, at, at); err != nil {
		t.Fatal(err)
	}
	got := captureInstallFacts(t, installOpts{cliDir: f.cliDir, cliVersion: "9.9.9", cliZst: blob})
	if want := "decompressing: invalid input: magic number mismatch"; got.CliError != want {
		t.Errorf("cliError = %q, want %q", got.CliError, want)
	}
	f.wantNames(t, "L3", "1.0.0.zst")
}

// A -cli-url body that is no zstd archive answers the decoder text, with the
// right and with a wrong checksum. fetch.bytes is the count that the decoder
// read, and only the leading progress line is printed. On 5fd08069 that is
// "fetch":{"bytes":4,…} for a body of 4096 bytes (cells L15a and L15b, Linux
// VM).
func TestInstallURLBodyThatIsNoArchive(t *testing.T) {
	body := []byte(strings.Repeat("not a zstd archive. ", 205))[:4096]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	for _, tc := range []struct{ id, checksum string }{
		{"L15a", sha256Hex(body)},
		{"L15b", sha256Hex([]byte("other"))},
	} {
		t.Run(tc.id, func(t *testing.T) {
			cliDir := filepath.Join(t.TempDir(), "cli")
			out := captureInstallOutput(t, installOpts{cliDir: cliDir, cliVersion: "9.9.9", cliURL: srv.URL, cliChecksum: tc.checksum})
			line := out[strings.LastIndex(out, "__INSTALL_RESULT__"):]
			if want := `"cliError":"decompressing: invalid input: magic number mismatch","fetch":{"bytes":4,`; !strings.Contains(line, want) {
				t.Errorf("result line %q, want %q", line, want)
			}
			if counts, _ := progressCounts(t, out); len(counts) != 1 || counts[0] != 0 {
				t.Errorf("progress byte counts = %v, want the leading line only", counts)
			}
			if ents, err := os.ReadDir(cliDir); err != nil || len(ents) != 0 {
				t.Errorf("cli folder entries %v, err %v, want it empty", ents, err)
			}
		})
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// sameFileBytes compares two files by SHA-256. A file that cannot be read is
// not the same. placeStagedCLI uses it on Windows only (cells W2 of 5fd08069).
func TestSameFileBytes(t *testing.T) {
	dir := t.TempDir()
	put := func(name, body string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a, b, c := put("a", "same"), put("b", "same"), put("c", "other")
	missing := filepath.Join(dir, "missing")
	for _, tc := range []struct {
		x, y string
		want bool
	}{{a, b, true}, {a, c, false}, {a, missing, false}, {missing, a, false}} {
		if got := sameFileBytes(tc.x, tc.y); got != tc.want {
			t.Errorf("sameFileBytes(%s, %s) = %v, want %v", filepath.Base(tc.x), filepath.Base(tc.y), got, tc.want)
		}
	}
}
