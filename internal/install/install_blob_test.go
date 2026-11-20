package install

import (
	"crypto/sha256"
	"encoding/binary"
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
// runs --version. On 5fd08069 the stub sees no blob at its run (measured on
// Linux, macOS and Windows). The blob lies in the cli folder here, so the
// folder list of the stub shows it.
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
	f.wantNames(t, "good install", filepath.Base(f.cli))
}

// A -cli-zst blob in the cli folder with a name of the sweep stays after a
// failed install. On 5fd08069 a blob C/1.0.0.zst that is 20 minutes old and no
// zstd archive stays, with the decoder text (measured on Linux).
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
	f.wantNames(t, "failed install", "1.0.0.zst")
}

// A -cli-url body that is no zstd archive answers the decoder text, with the
// right and with a wrong checksum. fetch.bytes is the count that the decoder
// read, and only the leading progress line is printed. On 5fd08069 that is
// "fetch":{"bytes":4,…} for a body of 4096 bytes (measured on Linux).
func TestInstallURLBodyThatIsNoArchive(t *testing.T) {
	body := []byte(strings.Repeat("not a zstd archive. ", 205))[:4096]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	for _, tc := range []struct{ id, checksum string }{
		{"the checksum matches", sha256Hex(body)},
		{"the checksum differs", sha256Hex([]byte("other"))},
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

// The cap off still refuses a zstd frame with a window over 512 MiB. A
// single-segment frame has its declared size as its window. At a declared 512 MiB
// the frame passes that test and fails on its short content. On 4c111e86 the
// texts are the same (measured on Linux, the single-segment rows on macOS and
// Windows too).
func TestInstallRefusesAZstdWindowOver512MiB(t *testing.T) {
	single := func(declared uint64) []byte {
		return binary.LittleEndian.AppendUint64([]byte{0xe0}, declared) // single segment, 8-byte size
	}
	for _, tc := range []struct {
		id     string
		header []byte
		want   string
	}{
		{"single segment, 512 MiB+1", single(512<<20 + 1), "decompressing: decompressed size exceeds configured limit"},
		{"single segment, 512 MiB", single(512 << 20), "decompressing: frame size does not match size on stream"},
		{"1 GiB window, no size", []byte{0x00, 0xa0}, "decompressing: window size exceeded"},
	} {
		blob := filepath.Join(t.TempDir(), "in.zst")
		if err := os.WriteFile(blob, zstdFrame(tc.header, 4096), 0o600); err != nil {
			t.Fatal(err)
		}
		cliDir := filepath.Join(t.TempDir(), "cli")
		got := captureInstallFacts(t, installOpts{cliDir: cliDir, cliVersion: "9.9.9", cliZst: blob})
		if got.CliError != tc.want {
			t.Errorf("%s: cliError = %q, want %q", tc.id, got.CliError, tc.want)
		}
	}
}

// zstdFrame is one zstd frame: the magic, the frame header after it, and one
// last raw block of n zero bytes.
func zstdFrame(header []byte, n int) []byte {
	b := append([]byte{0x28, 0xb5, 0x2f, 0xfd}, header...)
	h := uint32(n)<<3 | 1
	b = append(b, byte(h), byte(h>>8), byte(h>>16))
	return append(b, make([]byte, n)...)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// sameFileBytes compares two files by SHA-256. A file that cannot be read is
// not the same. placeStagedCLI uses it on Windows only (`5fd08069`, measured on Windows).
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
