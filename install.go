package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

type installOpts struct {
	cliDir, cliVersion, cliURL, cliChecksum, cliZst string
	cliKeep                                         int
}

// installFacts mirrors the __INSTALL_RESULT__ JSON the real binary prints, in the
// exact field order (probe-verified against a private -cli-dir). There is NO
// "platform" field, and arch is GOARCH ("amd64"), not "x64".
type installFacts struct {
	ServerVersion string `json:"serverVersion"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	Libc          string `json:"libc"`
	CliPath       string `json:"cliPath"`
	CliWasPresent bool   `json:"cliWasPresent"`
	CliError      string `json:"cliError,omitempty"`
}

func runInstall(o installOpts) {
	f := installFacts{
		ServerVersion: Version,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		Libc:          detectLibc(),
	}

	if o.cliDir != "" && o.cliVersion != "" {
		f.CliPath = filepath.Join(o.cliDir, o.cliVersion)
		// "present" requires the file to exist AND be runnable (real binary checks
		// `<cli> --version`). A freshly downloaded CLI leaves cliWasPresent false.
		if isRegularFile(f.CliPath) && isRunnable(f.CliPath) {
			// Cache hit: the reference touches the cli-dir at all only when it
			// attempts an install, so neither the orphan sweep nor the prune
			// runs here (probe-measured — a cache-hit run with 4 versions and
			// -cli-keep 3 left all four in place).
			f.CliWasPresent = true
		} else {
			err := ensureCLI(o, f.CliPath)
			if err != nil {
				f.CliError = err.Error()
			}
			// The sweep runs whether or not the install succeeded; the prune
			// runs only when it succeeded. Both probe-measured at 5db5e4a:
			//
			//	scenario                 sweep  prune
			//	cache hit                no     no
			//	install attempted+failed yes    no
			//	install succeeded        yes    yes
			sweepFetchTemps(o.cliDir)
			if err == nil && o.cliKeep > 0 {
				pruneCLI(o.cliDir, o.cliKeep)
			}
		}
	}

	b, _ := json.Marshal(f)
	fmt.Printf("__INSTALL_RESULT__%s\n", b)
}

// ensureCLI materializes the CLI binary from a .zst blob: either an already
// uploaded one (-cli-zst, SFTP fallback) or a download (-cli-url), verified
// against -cli-checksum, then zstd-decompressed to cliPath.
func ensureCLI(o installOpts, cliPath string) error {
	// Refuse a -cli-version that resolves outside -cli-dir, BEFORE anything
	// touches the filesystem. cliPath is filepath.Join(cliDir, cliVersion), and
	// Join cleans, so a version of "../victim" resolves to a sibling of cliDir —
	// which the os.RemoveAll below then deletes RECURSIVELY before installing
	// there. Measured: with -cli-version ../victim, a directory holding an
	// unrelated file was destroyed and replaced by the CLI binary.
	//
	// This is a DIVERGENCE, and a deliberate one: the reference does exactly the
	// same thing on the same input (measured at 5db5e4a — identical destruction,
	// identical facts frame). It is the same class as the remote-server.log
	// refusal documented in PROTOCOL.md — a claustrum-only hardening on an attack
	// path the reference was not measured on, invisible on every honest path.
	// The real client passes a bare version string ("1.0.86"), so a legitimate
	// cliPath is always a single component under cliDir and never trips this.
	//
	// Guarding here rather than at the RemoveAll gates every filesystem effect,
	// not just the destructive one, and reports through the existing cliError
	// field so the facts frame keeps its shape. Skipped when cliDir is unset —
	// then cliPath is not derived from a version and there is nothing to contain.
	if o.cliDir != "" && !withinDir(o.cliDir, cliPath) {
		return fmt.Errorf("cli version %q escapes the cli dir", o.cliVersion)
	}
	var zst []byte
	var err error
	switch {
	case o.cliZst != "":
		// SFTP fallback: the blob arrives over an already-authenticated channel,
		// so the reference NEVER verifies -cli-checksum here. Claustrum diverges as
		// an opt-in hardening (IMPROVEMENTS D1): when a -cli-checksum IS supplied we
		// verify it and reject a corrupt/tampered blob with the same "checksum
		// mismatch" error as the -cli-url path. An ABSENT/empty checksum stays
		// trusting — matching the reference — so honest callers are unaffected.
		if zst, err = os.ReadFile(o.cliZst); err != nil {
			return fmt.Errorf("opening input: %v", err)
		}
		if o.cliChecksum != "" {
			if err := verifyChecksum(zst, o.cliChecksum); err != nil {
				return err
			}
		}
	case o.cliURL != "":
		if zst, err = httpGet(o.cliURL); err != nil {
			return fmt.Errorf("download failed: %v", err)
		}
		// Downloads are verified UNCONDITIONALLY — an empty -cli-checksum still
		// fails ("expected= , actual=<sha>") — matching the reference.
		if err := verifyChecksum(zst, o.cliChecksum); err != nil {
			return err
		}
	default:
		return fmt.Errorf("cli %s missing and no --cli-url or --cli-zst provided", o.cliVersion)
	}
	// 0700, not 0755: the reference creates the whole cli-dir chain owner-only.
	// Probe-measured under umask 022 against 5db5e4a — every directory it makes
	// comes out drwx------, while claustrum's came out drwxr-xr-x. The installed
	// CLI file itself is 0755 on both.
	if err := os.MkdirAll(filepath.Dir(cliPath), 0o700); err != nil {
		// "mkdir cli dir: " prefix, measured against 5db5e4a — the reference
		// reports `mkdir cli dir: mkdir <path>: permission denied` where claustrum
		// emitted the bare Go error. This lands in cliError on the wire.
		return fmt.Errorf("mkdir cli dir: %v", err)
	}
	// Decompress, chmod, and verify at a temp path, then atomically rename into
	// place — so an interrupted install never leaves a half-written or non-runnable
	// cliPath (IMPROVEMENTS #4). The end state is identical to the reference's
	// in-place extract: cliPath appears only as a complete, 0755, verified binary,
	// with the same facts and the same "not runnable" error.
	// Stage under the reference's own temp name, ".fetch-<random>" in the cli-dir,
	// rather than "<cliPath>.tmp". Two reasons, both measured: the reference
	// creates exactly that (O_EXCL, mode 0600 — visible in an strace of its
	// install), and sweepFetchTemps already reaps ".fetch-*" but knew nothing
	// about our ".tmp", so claustrum's own interrupted-install litter was never
	// cleaned up while the reference's was.
	tmpFile, err := os.CreateTemp(filepath.Dir(cliPath), ".fetch-*")
	if err != nil {
		return fmt.Errorf("staging cli: %v", err)
	}
	tmp := tmpFile.Name()
	_ = tmpFile.Close()
	if err := zstdDecompress(zst, tmp); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("decompressing: %v", err)
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// Verify the extracted CLI actually runs; if not, discard the temp and report.
	if !isRunnable(tmp) {
		_ = os.Remove(tmp)
		return fmt.Errorf("installed cli at %s is not runnable", cliPath)
	}
	// Clear whatever is already at cliPath before renaming over it. rename(2)
	// refuses to replace a non-empty directory, so without this an occupied
	// destination fails the whole install: measured at 5db5e4a, the reference
	// removes the blocker and installs successfully while claustrum returned
	// `rename …: file exists` and left the blocker in place.
	if err := os.RemoveAll(cliPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("clearing stale dir at %s: %v", cliPath, err)
	}
	if err := os.Rename(tmp, cliPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// SFTP fallback: the uploaded .zst blob is consumed on success.
	if o.cliZst != "" {
		_ = os.Remove(o.cliZst)
	}
	return nil
}

// withinDir reports whether path lies strictly inside dir, lexically. "Strictly"
// matters: a path EQUAL to dir is rejected too, because -cli-version "." resolves
// cliPath to cliDir itself and would hand the whole cli-dir to os.RemoveAll.
//
// Lexical, not symlink-resolved. That is the same containment the escape it
// blocks is built from — filepath.Join cleans "..", so the attack is lexical —
// and EvalSymlinks would add a TOCTOU window plus a dependency on paths that may
// not exist yet. A nested version ("linux-x64/1.0.86") stays legal; only leaving
// the tree does not.
func withinDir(dir, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	if err != nil {
		return false
	}
	if rel == "." || rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// verifyChecksum returns a "checksum mismatch" error (byte-identical to the
// reference's -cli-url path) when sha256(zst) does not equal expected. The
// -cli-url path calls it unconditionally; the -cli-zst path only when a checksum
// is supplied (IMPROVEMENTS D1, an opt-in divergence from the reference).
func verifyChecksum(zst []byte, expected string) error {
	sum := sha256.Sum256(zst)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, expected) {
		return fmt.Errorf("checksum mismatch: expected=%s, actual=%s", expected, got)
	}
	return nil
}

// isRunnable reports whether `<path> --version` exits 0 (the real binary's CLI
// validity check). A generous timeout guards against a hung binary.
func isRunnable(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, path, "--version").Run() == nil
}

// maxCLIBytes caps the decompressed size written by zstdDecompress. A crafted
// .zst file can be tiny when compressed but expand to fill the remote disk;
// 512 MB is well above any realistic CLI binary. Set to a small value in tests.
var maxCLIBytes int64 = 512 * 1024 * 1024

// zstdDecompress decompresses a zstd blob to dest in-process, using the same
// library (klauspost/compress) the real binary embeds — no external zstd CLI.
func zstdDecompress(zst []byte, dest string) error {
	dec, err := zstd.NewReader(bytes.NewReader(zst))
	if err != nil {
		return err
	}
	defer dec.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	n, err := io.Copy(out, io.LimitReader(dec, maxCLIBytes+1))
	if err != nil {
		return err
	}
	if n > maxCLIBytes {
		return fmt.Errorf("decompressed CLI exceeds %d bytes", maxCLIBytes)
	}
	return nil
}

func httpGet(url string) ([]byte, error) {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCLIBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxCLIBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxCLIBytes)
	}
	return body, nil
}

// pruneCLI keeps the most-recent `keep` version files under cliDir (by mtime).
// sweepFetchTemps removes the reference's leftover download temporaries from the
// cli-dir. They are named ".fetch-<something>"; an interrupted install leaves
// them behind indefinitely, and claustrum's pruneCLI used to count them as CLI
// *versions*, so they consumed the -cli-keep budget and evicted real binaries —
// with four versions, three orphans and -cli-keep 3, every real version was
// deleted, including the one just installed.
//
// os.Remove per entry, matching the reference exactly: it clears files and EMPTY
// directories, and silently leaves a non-empty ".fetch-dir/" in place. That
// asymmetry is measured, not incidental — an empty orphan directory is swept, a
// populated one survives.
func sweepFetchTemps(cliDir string) {
	ents, err := os.ReadDir(cliDir)
	if err != nil {
		return
	}
	for _, e := range ents {
		// ".fetch-*" AND "*.zst": the reference sweeps both. Measured at 5db5e4a
		// with a stray "leftover.zst" in the cli-dir — the reference removed it,
		// claustrum kept it, and pruneCLI then counted it as a version and burned
		// a -cli-keep slot on it. An unrelated file ("README") survives on both.
		if strings.HasPrefix(e.Name(), ".fetch-") || strings.HasSuffix(e.Name(), ".zst") {
			_ = os.Remove(filepath.Join(cliDir, e.Name()))
		}
	}
}

func pruneCLI(cliDir string, keep int) {
	ents, err := os.ReadDir(cliDir)
	if err != nil {
		return
	}
	type ver struct {
		name string
		mod  int64
	}
	var vs []ver
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if fi, err := e.Info(); err == nil {
			vs = append(vs, ver{e.Name(), fi.ModTime().UnixNano()})
		}
	}
	sort.Slice(vs, func(i, j int) bool { return vs[i].mod > vs[j].mod })
	for i := keep; i < len(vs); i++ {
		_ = os.Remove(filepath.Join(cliDir, vs[i].name))
	}
}

func isRegularFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// lddProbeTimeout bounds the `ldd --version` libc probe. The reference daemon
// runs it with no deadline, so a stalled or hostile `ldd` resolved earlier in
// PATH hangs `-install` forever (reported upstream as HackerOne #3793023). We cap
// it and fall back to the default classification on timeout: identical output on
// every normal host (ldd returns in well under a second), defensive only when it
// would otherwise hang. This is a deliberate, attack-path-only divergence from the
// reference — normal-path frames and `__INSTALL_RESULT__` facts are unchanged.
// (The libc VALUE itself is a separate matter: see classifyLibc for the loader
// glob, and libc_other.go for why the probe does not run off linux at all.)
const lddProbeTimeout = 5 * time.Second

// runLddVersion runs `ldd --version` under ctx; the process is killed if ctx expires.
func runLddVersion(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "ldd", "--version").CombinedOutput()
}

// detectLibcWith runs the libc probe under a deadline, then classifies the result.
// The timeout and runner are injected so the timeout/fallback path is exercisable
// on any host (mirroring classifyLibc's injectable glob).
func detectLibcWith(timeout time.Duration, run func(context.Context) ([]byte, error)) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := run(ctx)
	return classifyLibc(out, err, filepath.Glob)
}

// muslLoaderGlob matches the musl dynamic loader for ANY architecture. The
// reference carries this glob; claustrum used to stat a hardcoded
// "/lib/ld-musl-x86_64.so.1", which cannot see the loader on arm64 or riscv.
const muslLoaderGlob = "/lib/ld-musl-*.so.*"

// classifyLibc maps an `ldd --version` result to "musl" or "glibc". It is split
// from detectLibc with an injectable glob so both branches are testable on any
// host — a glibc box can't otherwise reach the musl paths.
//
// The loader glob is consulted FIRST and outranks ldd, which is what the
// reference does. Measured on a mixed host carrying glibc's ldd and a musl
// loader together: `ldd --version` reports "Debian GLIBC 2.41" and succeeds, yet
// the reference still answers "musl" — so the marker decides, and a successful
// glibc ldd does not veto it. claustrum consulted the marker only when ldd
// FAILED, so it answered "glibc" there, 3 runs out of 3.
//
// Clean single-libc hosts are unaffected and were identical before this change
// (Alpine → musl, Debian → glibc, container-verified): a Debian box has no musl
// loader to match, and an Alpine box is caught by either rule.
func classifyLibc(lddOut []byte, lddErr error, glob func(string) ([]string, error)) string {
	if m, err := glob(muslLoaderGlob); err == nil && len(m) > 0 {
		return "musl"
	}
	if lddErr == nil && strings.Contains(strings.ToLower(string(lddOut)), "musl") {
		return "musl"
	}
	return "glibc"
}
