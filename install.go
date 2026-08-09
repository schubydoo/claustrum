package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
			// runs here.
			//
			// The citation used to be "a cache-hit run with 4 versions and
			// -cli-keep 3 left all four in place". That fixture supports the
			// PRUNE half only — it contained no orphan litter, so it could not
			// have observed a sweep either way. Re-probed with a `.fetch-orphan`
			// and a `leftover.zst` present: both survive a cache hit, so the
			// sweep half is now supported too. Value was right, citation was not.
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
	// Validate -cli-version BEFORE anything touches the filesystem. The rules and
	// their measurements live on validateCLIVersion; both are claustrum-only
	// hardening, and on each input the reference does the damaging thing.
	//
	// Guarding here rather than at the individual hazards gates every filesystem
	// effect, not just the destructive one, and reports through the existing
	// cliError field so the facts frame keeps its shape. Skipped when cliDir is
	// unset — then cliPath is not derived from a version and there is nothing to
	// contain.
	if o.cliDir != "" {
		if err := validateCLIVersion(o.cliVersion); err != nil {
			return err
		}
	}
	// blobPath is the .zst on disk. Nothing HERE reads it into memory: the local
	// path uses the caller's file as-is, the download streams to a temp file, and
	// both are hashed and decompressed in bounded passes. The download's temp is
	// removed by a defer on that branch, so it cannot outlive the call.
	//
	// Measured with a 400 MiB incompressible blob (peak RSS from /proc VmHWM):
	//
	//	-cli-zst   410 MB -> 9 MB
	//	-cli-url   886 MB -> 10 MB   (ReadAll peaked at ~2x the body while growing)
	//
	// After this, peak memory is flat in the blob size. Control: the same 400 MiB
	// as zeros, a 14 KB blob, is 9 MB on both binaries — so the post-change number
	// is baseline, not workload.
	var blobPath, blobSum string
	var err error
	switch {
	case o.cliZst != "":
		// SFTP fallback: the blob arrives over an already-authenticated channel,
		// so the reference NEVER verifies -cli-checksum here. Claustrum diverges as
		// a conditional hardening (IMPROVEMENTS D1), activated by the caller supplying
		// -cli-checksum rather than by an operator: when a -cli-checksum IS supplied we
		// verify it and reject a corrupt/tampered blob with the same "checksum
		// mismatch" error as the -cli-url path. An ABSENT/empty checksum stays
		// trusting — matching the reference — so honest callers are unaffected.
		//
		// Opened and read one byte, purely to keep `opening input: ` attached to
		// the same conditions os.ReadFile reported it for (missing, permission,
		// is-a-directory). The read is not optional: os.Open SUCCEEDS on a
		// directory on both Unix and Windows — EISDIR surfaces on the first Read —
		// and os.ReadFile opened AND read, so an open alone would move
		// `-cli-zst <dir>` to `decompressing: `. That is the DEFAULT -cli-zst
		// shape (no -cli-checksum, which is what the reference does), and it is
		// provokable with mkdir. One byte reproduces the condition in the same
		// place with the OS's own wording. io.EOF is not a failure: os.ReadFile
		// succeeds on an empty file.
		//
		// A mid-read I/O error still surfaces later, at decompress, as
		// `decompressing: ` — an unprovoked path, recorded rather than claimed
		// identical.
		f, oerr := os.Open(o.cliZst)
		if oerr != nil {
			return fmt.Errorf("opening input: %v", oerr)
		}
		_, rerr := f.Read(make([]byte, 1))
		_ = f.Close()
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return fmt.Errorf("opening input: %v", rerr)
		}
		blobPath = o.cliZst
		if o.cliChecksum != "" {
			if blobSum, err = sha256File(blobPath); err != nil {
				return fmt.Errorf("opening input: %v", err)
			}
			if err := verifyChecksum(blobSum, o.cliChecksum); err != nil {
				return err
			}
		}
	case o.cliURL != "":
		// The temp lands beside the CLI destination when it can; see fetchToFile
		// for why a failure there falls back rather than erroring.
		blobPath, blobSum, err = fetchToFile(o.cliURL, filepath.Dir(cliPath))
		if err != nil {
			// A status failure is already fully worded; only a transport failure
			// takes the "download failed: " prefix (see httpStatusError).
			var se *httpStatusError
			if errors.As(err, &se) {
				return se
			}
			return fmt.Errorf("download failed: %v", err)
		}
		defer func() { _ = os.Remove(blobPath) }()
		// Downloads are verified UNCONDITIONALLY — an empty -cli-checksum still
		// fails ("checksum mismatch: expected=, actual=<sha>") — matching the
		// reference. That string is verifyChecksum's own output, captured; the
		// quote here used to read "expected= , actual=<sha>", which neither binary
		// emits — it had a space that is not there and dropped the prefix.
		//
		// blobSum came from the download stream, so verifying costs no second read.
		if err := verifyChecksum(blobSum, o.cliChecksum); err != nil {
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
	// Stage, verify and install, retried ONCE if a concurrent install's sweep
	// reclaimed our staging file. See stageAndInstall for why that can happen and
	// why a retry is the fix rather than a smarter sweep.
	decompressed, err := stageAndInstall(blobPath, cliPath)
	if err != nil && errors.Is(err, errStagingVanished) {
		// Accumulate rather than overwrite. Decompression is a fact about the
		// blob, not about an attempt: once any attempt has decompressed it, the
		// consume rule below is satisfied for good. Assigning here instead would
		// let a retry that fails BEFORE its own decompress (a CreateTemp or write
		// error) report false and keep a blob the first attempt had already
		// decompressed — contradicting the rule stated right below.
		//
		// Not shown to be reachable: the sweep that triggers the retry removes
		// `.fetch-*`, not the cli-dir, so a second-pass CreateTemp failure needs
		// state the retry path does not itself produce. This is invariant
		// hygiene, and it costs one variable.
		retryDecompressed, retryErr := stageAndInstall(blobPath, cliPath)
		decompressed = decompressed || retryDecompressed
		err = retryErr
	}
	// The uploaded .zst blob is consumed once DECOMPRESSION SUCCEEDED — not only
	// on a fully successful install.
	//
	// Measured against 5db5e4a with four fixtures, which bracket the boundary
	// tightly around the decompress step. The "claustrum" column below is the
	// PRE-FIX state that motivated this rule — it is what claustrum did before the
	// consume condition was changed to key on decompression, NOT what it does now:
	//
	//	extracted CLI runs           reference consumed   claustrum consumed
	//	extracted CLI exits 1        reference CONSUMED   claustrum kept  <- fixed
	//	blob is not valid zstd       reference kept       claustrum kept
	//	blob does not exist          nothing to consume on either
	//
	// Today claustrum consumes wherever decompression succeeded, so row two now
	// matches the reference — which is the whole point of the change.
	//
	// So a failure BEFORE decompression succeeds leaves the blob alone, and a
	// failure after it does not. (Not "before the staged file exists":
	// os.CreateTemp makes that file before zstdDecompress runs, so the bad-zstd
	// row has a staged file and still keeps the blob.) The cliError strings are byte-identical
	// on all four. The failures between decompression and rename (chmod, the
	// destination clear) were not provoked; they sit on the consumed side of the
	// measured boundary by construction, not by observation.
	if o.cliZst != "" && decompressed {
		_ = os.Remove(o.cliZst)
	}
	return err
}

// errStagingVanished marks the one failure ensureCLI retries: the staging file
// was removed by another process between its creation and the rename.
var errStagingVanished = errors.New("staging file vanished")

// stageAndInstall decompresses zst to a staging file beside cliPath, verifies it
// runs, and renames it into place.
//
// The staging step ITSELF is a pre-existing claustrum divergence (IMPROVEMENTS
// #4). Here cliPath only ever appears as a complete, 0755, verified binary. The
// end state is identical — same facts, same "not runnable" error — so nothing on
// the wire changes. Everything below about losing a staging file follows from that
// choice, not from a new one.
//
// This used to say flatly that "the reference extracts in place". Measured
// 2026-08-08 on -cli-url, that is wrong as a general statement: mid-download the
// reference's cli-dir holds a ".fetch-<random>" whose first bytes are the
// DECOMPRESSED CLI's. What the original measurement actually established is
// narrower and still holds — across the post-extraction --version window the
// reference shows only the installed version, where claustrum still holds a
// staging file. So the windows differ, not the presence of staging as such, and
// the divergence this comment describes is about how long cliPath's replacement
// stays incomplete. The probe-window half was measured on BOTH source paths; the
// mid-download finding is -cli-url only.
//
// Staged under the reference's own temp name, ".fetch-<random>" in the cli-dir,
// rather than "<cliPath>.tmp": sweepFetchTemps reaps ".fetch-*" but knew nothing
// about a ".tmp", so claustrum's own interrupted-install litter was never cleaned
// up while the reference's was.
//
// That choice has a cost the reference does not pay in ONE window. Measured with
// a CLI that sleeps 3s on --version, claustrum shows ".fetch-XXXX" in the cli-dir
// for that whole probe window while the reference shows only the installed
// version, on both the -cli-zst and -cli-url paths. So across the probe window
// the reference has no in-flight file for its own sweep to hit, and claustrum
// does: any concurrent install that finishes first sweeps ours.
//
// This used to say the reference's sweep "can NEVER hit its own in-flight file".
// That is too strong, and a different window falsifies it: measured 2026-08-08
// mid-DOWNLOAD against a deliberately slow origin, the reference's cli-dir holds
// a ".fetch-<random>" of its own (decompressed output, by its first bytes). The
// original measurement only ever looked at the post-extraction probe window, so
// it could not have seen this. Claustrum's exposure is the STAGING window —
// decompress, chmod, probe, rename — not the download, and the retry below covers
// all of it, since a loss is only detected at the rename anyway. The download blob
// is covered separately, by being outside isSweptName so no sweep claims it.
// The reference's own exposure is the reference's to carry.
//
// The caller retries once on errStagingVanished, which is what actually fixes
// that. A smarter sweep cannot: skipping entries that postdate our own start
// still loses the staggered case, where the other install staged BEFORE we began
// and its file is indistinguishable from litter by name alone. Recovering from
// the loss is exact; predicting which files are safe to delete is not — and the
// retry lets the sweep stay unconditional, matching the reference.
// chmodStaged is os.Chmod behind a seam, so the one branch between decompress
// and rename that no fixture can otherwise provoke is reachable from a test.
// That branch matters more than its size: it is on the CONSUMED side of the
// blob rule, and until it was exercised the PR could only claim so by
// construction. Production never reassigns it.
var chmodStaged = os.Chmod

func stageAndInstall(blobPath, cliPath string) (decompressed bool, err error) {
	tmpFile, err := os.CreateTemp(filepath.Dir(cliPath), ".fetch-*")
	if err != nil {
		return false, fmt.Errorf("staging cli: %v", err)
	}
	tmp := tmpFile.Name()
	_ = tmpFile.Close()
	if err := zstdDecompress(blobPath, tmp); err != nil {
		_ = os.Remove(tmp)
		return false, fmt.Errorf("decompressing: %v", err)
	}
	if err := chmodStaged(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return true, err
	}
	// Verify the extracted CLI actually runs; if not, discard the temp and report.
	if !isRunnable(tmp) {
		_ = os.Remove(tmp)
		return true, fmt.Errorf("installed cli at %s is not runnable", cliPath)
	}
	// Clear the destination ONLY when it is a directory, then rename into place.
	//
	// Only a directory blocks rename(2) — a regular file is replaced atomically —
	// so a directory is the only case that needs clearing, and it is the same case
	// the reference clears: measured at 5db5e4a, the reference removes the blocker
	// and installs successfully while claustrum returned `rename …: file exists`
	// and left it in place. End states match the reference for every destination
	// shape (absent / regular file / non-empty directory).
	//
	// The narrowness is the point. Clearing unconditionally destroys the
	// destination BEFORE knowing the staging file survived, so a swept staging
	// file left an installed, working CLI deleted and nothing put back. An
	// installed CLI is always a regular file (the cache-hit check requires it), so
	// it is never what gets cleared here; a directory at cliPath is a stale
	// blocker, not an install.
	if fi, err := os.Lstat(cliPath); err == nil && fi.IsDir() {
		if rmErr := os.RemoveAll(cliPath); rmErr != nil {
			_ = os.Remove(tmp)
			return true, fmt.Errorf("clearing stale dir at %s: %v", cliPath, rmErr)
		}
	}
	if err := os.Rename(tmp, cliPath); err != nil {
		if _, statErr := os.Lstat(tmp); statErr != nil {
			// Our staging file is gone — a concurrent install's sweep took it.
			// cliPath is deliberately left alone; there is nothing to install.
			return true, fmt.Errorf("%w before install: %v", errStagingVanished, err)
		}
		_ = os.Remove(tmp)
		return true, err
	}
	return true, nil
}

// validateCLIVersion rejects a -cli-version the install cannot honestly carry
// out. Two rules, both measured, both claustrum-only hardening — the reference
// accepts each input and does the damaging thing.
//
//  1. A SINGLE PATH COMPONENT. cliPath is filepath.Join(cliDir, cliVersion) and
//     ensureCLI's os.RemoveAll deletes cliPath recursively, so a version that
//     reaches outside cliDir destroys unrelated data. "../victim" escapes because
//     Join CLEANS; "link/1.0.0" escapes through a symlink under cliDir, which a
//     lexical containment check accepts because it is lexically inside.
//
//  2. NOT A NAME THE SWEEP CLAIMS. sweepFetchTemps runs after every attempted
//     install and removes ".fetch-*" and "*.zst". A version of ".fetch-x" or
//     "1.0.zst" therefore installs correctly and is deleted moments later, in the
//     same run, while the facts frame reports success with no cliError. Measured
//     at 5db5e4a: reference AND claustrum both leave an EMPTY cli-dir and report
//     no error. Reporting an error beats reporting a success that installed
//     nothing, so claustrum refuses the version instead.
//
// "." and ".." are rejected explicitly by rule 1: "." resolves cliPath to the
// cli-dir ITSELF, which would hand the whole cli-dir to os.RemoveAll.
//
// BOTH separators are rejected on every OS, not just the local one. A backslash
// is a legal filename byte on Unix, so this is stricter than the platform
// requires — deliberately, so a Unix daemon cannot be handed a path that a
// Windows client built, and so the accepted set does not change with GOOS.
//
// No real version string trips either rule: 1.0.86, 2.0.0-beta.1, a commit sha,
// "latest" and 1.0.86+build.5 are all measured as accepted.
func validateCLIVersion(v string) error {
	if !isSingleComponent(v) {
		return fmt.Errorf("cli version %q must be a single path component", v)
	}
	if isSweptName(v) {
		// Deliberately names the sweep, not the character class: the point is the
		// collision, and isSweptName is the one definition of it.
		return fmt.Errorf("cli version %q collides with the install temp sweep", v)
	}
	if isDownloadBlobName(v) {
		// The MIRROR of the check above, on the identical input. A version with
		// the blob prefix installs fine and is then exempt from pruneCLI's census
		// FOREVER — never counted against -cli-keep, never evicted, and not
		// reclaimed by the sweep either, since neither pass claims that prefix by
		// construction. A leaked immortal binary rather than a deleted one, but
		// the same class of collision, so the validator reads the same set of
		// housekeeping names both passes do.
		return fmt.Errorf("cli version %q collides with the install download blob", v)
	}
	return nil
}

// isSingleComponent reports whether name is one ordinary path element — a name
// that can only ever resolve to a direct child of the directory it is joined to.
func isSingleComponent(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, `/\`)
}

// verifyChecksum returns a "checksum mismatch" error (byte-identical to the
// reference's -cli-url path) when got does not equal expected. The -cli-url path
// calls it unconditionally; the -cli-zst path only when a checksum is supplied
// (IMPROVEMENTS D1, a conditional divergence from the reference — caller-activated).
//
// It takes the hex digest rather than the bytes so no caller has to hold the
// whole blob in memory to check it: the download hashes as it streams, and the
// local path hashes the file in one bounded pass (sha256File).
func verifyChecksum(got, expected string) error {
	if !strings.EqualFold(got, expected) {
		return fmt.Errorf("checksum mismatch: expected=%s, actual=%s", expected, got)
	}
	return nil
}

// sha256File returns the hex sha256 of a file, read in fixed-size chunks so a
// large blob never lands in memory.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// cliProbeTimeout bounds the `<cli> --version` runnability probe in isRunnable.
//
// ZERO (the default) DISABLES IT, which is the parity position: the reference
// showed no deadline at or below 45 s against a CLI that never answers, and it
// INSTALLED a CLI that answers in 90 s, waiting 91 s for it.
//
// ⚠️ A deadline is NOT a hang detector. It cannot separate "never answers" from
// "answers slowly", so any non-zero value rejects some honest CLI. Measured at
// 5db5e4a with a CLI that sleeps 20 s, prints its version and exits 0: the
// reference installs it, while claustrum with the old hardcoded 15 s answered
//
//	cliError "installed cli at <path> is not runnable"
//
// and deleted the staged binary, leaving the cli-dir empty. That is why raising
// the constant was rejected in favour of disabling it — every finite deadline
// invents a boundary the reference was not observed to have — measured only at or
// below 90 s; above that it is unmeasured on this path.
//
// The probe shipped bounded at a hardcoded 15 s and is the sibling of the
// files.extract_tar and -install size caps, which were flipped the same way in
// PRs 236 and 238. Opt in with -cli-probe-timeout or the cli-probe-timeout key
// in claustrum.conf; the config key is the reachable one, because Claude Desktop
// owns the argv on -install. Also set directly by tests. Divergence D11.
var cliProbeTimeout time.Duration

// isRunnable reports whether `<path> --version` exits 0 (the real binary's CLI
// validity check).
//
// With cliProbeTimeout disabled the probe runs with NO deadline at all — the
// context is not created, rather than created with a large timeout. Same rule as
// the two size caps: the bypass is the parity behaviour, and a huge-but-finite
// deadline is a different thing that merely looks equivalent.
func isRunnable(path string) bool {
	if cliProbeTimeout <= 0 {
		return exec.Command(path, "--version").Run() == nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), cliProbeTimeout)
	defer cancel()
	return exec.CommandContext(ctx, path, "--version").Run() == nil
}

// maxCLIBytes caps two install-path reads: the decompressed size written by
// zstdDecompress, and the downloaded body in fetchToFile. A crafted .zst can be
// tiny compressed and expand to fill the remote disk; the cap bounds that.
//
// ZERO (the default) DISABLES IT, which is what the reference does at every size
// the probe could reach. Measured at 5db5e4a with a 600 MiB payload (21 KB
// compressed) on the -cli-zst path: the reference decompressed all of it and got
// as far as the runnability check, answering
//
//	cliError "installed cli at <path> is not runnable"
//
// while a capped claustrum answered
//
//	cliError "decompressing: decompressed CLI exceeds 536870912 bytes"
//
// — a string the reference cannot produce. The cap shipped on by default at
// 512 MiB (PRs 57 and 59) and is the sibling of the files.extract_tar cap, which
// gets the same flip in its own PR. Opt in with -max-cli-bytes or the
// max-cli-bytes key in claustrum.conf. Also set directly by tests.
//
// The -cli-url half was measured separately (the download body, 629 MB
// incompressible): same result, with a cap-on control answering "response
// exceeds 536870912 bytes" to prove the probe reached this limit.
var maxCLIBytes int64

// zstdDecompress decompresses the zstd blob at src to dest in-process, using the
// same library (klauspost/compress) the real binary embeds — no external zstd CLI.
//
// It takes a PATH rather than a []byte for two reasons: the blob never has to be
// held in memory, and ensureCLI retries stageAndInstall once, which needs a
// source it can read a second time.
func zstdDecompress(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	dec, err := zstd.NewReader(in)
	if err != nil {
		return err
	}
	defer dec.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	// Cap disabled (the default): copy straight through, as the reference does.
	// Deliberately NOT a LimitReader with a huge bound — the cap+1 arithmetic is
	// what defines the boundary, and routing the unlimited case through it would
	// invent one the reference does not have.
	var n int64
	if maxCLIBytes <= 0 {
		n, err = io.Copy(out, dec)
	} else {
		n, err = io.Copy(out, io.LimitReader(dec, maxCLIBytes+1))
	}
	if err != nil {
		return err
	}
	if maxCLIBytes > 0 && n > maxCLIBytes {
		return fmt.Errorf("decompressed CLI exceeds %d bytes", maxCLIBytes)
	}
	return nil
}

// httpStatusError is a non-200 response from the CLI download. It exists so the
// caller can tell a STATUS failure from a TRANSPORT failure: the reference words
// them differently, prefixing only the latter with "download failed: ".
//
//	transport : download failed: Get "http://…": dial tcp …: connection refused
//	status    : download failed with status 404
type httpStatusError struct{ code int }

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("download failed with status %d", e.code)
}

// cliDownloadTimeout bounds the whole `-cli-url` exchange in fetchToFile.
//
// ZERO (the default) DISABLES IT, which is the parity position: measured at
// 5db5e4a against a server that sends 200 OK with a Content-Length and then never
// sends a body, the reference was still downloading when the harness stopped it at
// 400 s, where claustrum returned at 300 s.
//
// ⚠️ `http.Client.Timeout` bounds the ENTIRE exchange including the body read, so
// it is a deadline, not a stall detector: a real body arriving over a link that
// needs six minutes trips it exactly as a black hole does, and one that finishes
// in 4:59 does not. That is the same threshold-not-intent problem D3 and D10 were
// flipped for, and Claude Desktop owns the argv on `-install`, so the caller who
// pays cannot decline. (D11's runnability probe has the same shape and took the
// same flip; both are opt-in and default-off now.)
//
// Zero is the stdlib's own "no timeout" sentinel, so assigning it straight through
// IS the bypass — no huge-but-finite value stands in for "off", which is the same
// property D3 and D10 get by skipping their `io.LimitReader`s.
//
// ⚠️ That disables the bound on the BODY READ, not every clock on the path.
// fetchToFile leaves Transport nil, so it uses http.DefaultTransport, which
// carries net.Dialer{Timeout: 30s} and TLSHandshakeTimeout: 10s. A host that
// black-holes SYN still fails at 30 s with the bound "off". Those are stdlib
// defaults, always-on, and unnumbered — neither has been probed on the reference.
// Opt in with
// -cli-download-timeout or the cli-download-timeout key in claustrum.conf. Also
// set directly by tests. Divergence D12.
var cliDownloadTimeout time.Duration

// fetchToFile downloads url to a temporary file and returns that file's path
// together with the sha256 computed WHILE streaming, so the blob is never held in
// memory and is never hashed in a second pass. The caller owns the temp file and
// must remove it.
//
// The temp is preferentially created beside the CLI destination (dir), which puts
// it on the filesystem the install writes to. If that fails — an unwritable or
// not-yet-created cli-dir — it falls back to the OS temp dir rather than
// reporting a download failure, because ensureCLI creates the cli-dir AFTER the
// download and reports its own `mkdir cli dir: ` error there. Failing here
// instead would move that error to a different string on a reachable path.
func fetchToFile(url, dir string) (path, sum string, err error) {
	client := &http.Client{Timeout: cliDownloadTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// "download failed with status %d" — the reference's exact wording, and it
		// carries neither the URL nor the reason phrase. Measured at 5db5e4a: a 404
		// gives cliError "download failed with status 404" where claustrum emitted
		// "download failed: download <url>: 404 File not found". The URL is worth
		// omitting on its own merits: cliError is printed on the __INSTALL_RESULT__
		// line, and a signed URL would land in whatever captures that output.
		return "", "", &httpStatusError{code: resp.StatusCode}
	}
	// ⚠️ The prefix must be one isSweptName does NOT claim. sweepFetchTemps runs
	// after EVERY attempted install and takes ".fetch-*" and "*.zst" from the
	// cli-dir, so naming the blob ".fetch-*" would let a concurrent install's
	// sweep delete the staging file AND the blob the retry re-reads — defeating
	// errStagingVanished in exactly the case it exists for, and able to fail the
	// first attempt too if the sweep lands between here and zstdDecompress. That
	// invariant is what the retry's correctness now rests on.
	//
	// Kept beside the destination rather than moved to the OS temp dir: /tmp is a
	// tmpfs on many hosts, so downloading a 600 MiB blob there would put it back
	// in RAM and undo the streaming this change exists for. The cost is that the
	// sweep can no longer reclaim a blob left by a SIGKILLed install; the caller's
	// own defer removes it on every other path.
	//
	// ⚠️ On a FIRST install that argument does not hold, and the fallback below is
	// why: ensureCLI creates the cli-dir AFTER this runs, so the CreateTemp here
	// fails with ENOENT and the blob goes to $TMPDIR after all — onto the tmpfs
	// this paragraph exists to avoid, and out of reach of the sweep and the prune.
	// Peak RSS stays flat either way (tmpfs pages are page cache, not process
	// RSS), so the 886 MB -> 10 MB figure is unaffected; the host is still holding
	// the blob in memory though, and that number alone does not say so.
	f, err := os.CreateTemp(dir, blobTempPrefix+"*")
	if err != nil {
		if f, err = os.CreateTemp("", "claustrum-fetch-*"); err != nil {
			return "", "", err
		}
	}
	tmp := f.Name()
	h := sha256.New()
	// Same bypass as zstdDecompress: with the cap off the body streams straight
	// through, hashed on the way past.
	var n int64
	var copyErr error
	if maxCLIBytes <= 0 {
		n, copyErr = io.Copy(io.MultiWriter(f, h), resp.Body)
	} else {
		n, copyErr = io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxCLIBytes+1))
	}
	closeErr := f.Close()
	switch {
	case copyErr != nil:
		_ = os.Remove(tmp)
		return "", "", copyErr
	case closeErr != nil:
		_ = os.Remove(tmp)
		return "", "", closeErr
	case maxCLIBytes > 0 && n > maxCLIBytes:
		_ = os.Remove(tmp)
		return "", "", fmt.Errorf("response exceeds %d bytes", maxCLIBytes)
	}
	return tmp, hex.EncodeToString(h.Sum(nil)), nil
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
// UNCONDITIONAL, matching the reference. An earlier version skipped entries that
// appeared after the install started, to avoid reclaiming a concurrent install's
// in-flight staging file. That was a divergence AND only a partial guard — in the
// staggered case the other install staged BEFORE this one began, so its file is
// indistinguishable from litter by name alone. stageAndInstall's retry handles
// the loss instead, which is exact and lets this stay identical to the reference.
func sweepFetchTemps(cliDir string) {
	ents, err := os.ReadDir(cliDir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if isSweptName(e.Name()) {
			_ = os.Remove(filepath.Join(cliDir, e.Name()))
		}
	}
}

// blobTempPrefix names the -cli-url download blob. It must be a prefix that
// NEITHER cli-dir housekeeping pass acts on — isSweptName must not claim it, and
// pruneCLI must not count it as a version. Both halves matter and they failed one
// at a time: ".fetch-" let the sweep delete the blob out from under the
// errStagingVanished retry, and a name merely absent from isSweptName still let
// pruneCLI census it, where an in-flight blob sorts newest, burns a -cli-keep
// slot and evicts a real version instead.
//
// This is claustrum's problem to state because claustrum is what creates the
// file. What the reference does mid-download was measured 2026-08-08 and is
// recorded in PROTOCOL (Staging and cleanup): its cli-dir holds a
// ".fetch-<random>" whose first bytes are the DECOMPRESSED CLI's, where
// claustrum's staging file at that moment holds the compressed body. Different
// artifacts, so the naming rule below is claustrum's own either way. Defined
// once here so the creator, BOTH housekeeping passes and validateCLIVersion read
// the same rule — the same reason isSweptName is factored out below, and with the
// same fourth reader. A rule the validator does not consult is one an operator can
// walk into with -cli-version.
const blobTempPrefix = ".blob-"

// isDownloadBlobName reports whether a cli-dir entry is an in-flight download
// blob, which is neither litter to sweep nor a CLI version to prune.
func isDownloadBlobName(name string) bool { return strings.HasPrefix(name, blobTempPrefix) }

// isSweptName reports whether the sweep above claims a cli-dir entry.
//
// ".fetch-*" AND "*.zst": the reference sweeps both. Measured at 5db5e4a with a
// stray "leftover.zst" in the cli-dir — the reference removed it, claustrum kept
// it, and pruneCLI then counted it as a version and burned a -cli-keep slot on
// it. An unrelated file ("README") survives on both.
//
// Split out so the sweep and the -cli-version validator read the SAME rule. A
// version matching this predicate installs successfully and is then deleted by
// the sweep in the same run, so the two must not drift apart — see
// validateCLIVersion.
func isSweptName(name string) bool {
	return strings.HasPrefix(name, ".fetch-") || strings.HasSuffix(name, ".zst")
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
		// A concurrent install's in-flight download blob is not a CLI version.
		// It is the only file claustrum creates in the cli-dir that survives the
		// sweep, so without this it would sort NEWEST, take a -cli-keep slot and
		// evict a real binary — the exact defect isSweptName was introduced to
		// stop for .fetch-* staging files.
		if isDownloadBlobName(e.Name()) {
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

// lddProbeTimeout bounds the `ldd --version` libc probe WHEN SET; it is 0 by
// default and then bounds nothing. This is divergence D14; see IMPROVEMENTS.md for
// the measured tables.
//
// MEASURED: the reference applies no deadline here at or below 45 s — probed with
// a stub ldd on PATH and the musl loader glob masked. That result comes from the
// `exec sleep 120` shape below ONLY; the surviving-child shape cannot support it,
// because the PRE-FLIP claustrum had a deadline and looked identical there. An earlier version
// of this comment said the unbounded wait was "assumed"; it is measured now, in
// that one shape. When a deadline IS set we cap the probe and fall back to the
// default classification on timeout; at the shipped default none is set — see the
// D14 FLIP paragraph at the bottom of this comment, which is the current default and
// not an afterthought.
//
// A wall-clock deadline cannot separate a hostile `ldd` from a slow one, so an
// honest-but-slow `ldd` falls back too — but that usually changes NOTHING
// observable: on a musl host detectLibcWith returns "musl" from the loader glob
// without ever spawning ldd, and on a glibc host the fallback IS "glibc". The
// reported value moves only where ldd says musl AND exits 0 and the loader glob
// misses. The exit code is load-bearing: a faithful musl `ldd --version` prints to
// stderr and exits 1, and classifyLibc then falls back to "glibc" regardless.
// ⚠️ That residual is narrow but NOT costless: Claude Desktop uses the reported
// libc to pick which CLI build it downloads, so on that one host shape the
// consequence is the wrong build being fetched, not a cosmetic field. ⚠️ That last
// sentence is a claim about the DRIVER, not about the reference daemon — the
// parity harness cannot confirm or refute it. See docs/ARCHITECTURE.md →
// Driver claims and their provenance.
//
// 🔴 The bound is narrower than it reads, and this comment used to overstate it by
// calling the divergence "total". MEASURED, against a STALLED ldd:
//
//	stub `exec sleep 120` (nothing survives the kill) -> claustrum falls back at 5s
//	  and prints a complete __INSTALL_RESULT__; the reference emits nothing at 45s.
//	  ⚠️ The claustrum column is the PRE-FLIP always-on build (the retracted 5s
//	  default). At the shipped default it does not fall back at all and this row's
//	  claustrum column becomes the reference's.
//	stub `sleep 120` (a child survives holding the output pipe) -> NEITHER binary
//	  replies at 45s (measured). For CLAUSTRUM the cause is CombinedOutput waiting
//	  on the inherited pipe after the deadline kills the shell — the same softness
//	  gitTimeout has (see methods_git.go). The REFERENCE's cause is unmeasured;
//	  this arm cannot show it, and no mechanism is claimed for it here.
//
// A hostile ldd answering in 1s is untouched by the deadline either way.
// (The libc VALUE itself is a separate matter and is still unmeasured: see
// classifyLibc for the loader glob, libc_other.go for why the probe does not run
// off linux at all, and IMPROVEMENTS tier item 5 for the musl-banner fixture that
// would settle it.)
// D14 FLIP: ZERO (the default) DISABLES IT, which is the parity position. Opt in
// with -libc-probe-timeout or the libc-probe-timeout key in claustrum.conf; the
// config key is the reachable one, since Claude Desktop owns the -install argv.
// Disabled bypasses context.WithTimeout ENTIRELY (see lddCtx) rather than passing a
// huge duration, for the same reason D3 and D10 bypass their io.LimitReaders and D5
// bypasses gitCtx: an unarmed cancel path is what makes exec.CommandContext have
// nothing to fire.
//
// ⚠️ What turning it OFF costs, stated plainly: against a stalled `ldd` claustrum
// now waits as the reference does, so `-install` can block indefinitely instead of
// falling back at 5 s. That is the trade — it is the reference's measured behaviour
// (no reply at 45 s in the discriminating shape), and the caller who wants the bound
// can ask for it. It was flipped rather than argued for because the honest-path cost
// was never measured in EITHER direction and the caller could not decline it; D4 is
// the precedent, where the cost WAS measured and it was flipped anyway.
var lddProbeTimeout time.Duration

// runLddVersion runs `ldd --version` under ctx; the process is killed if ctx expires.
func runLddVersion(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "ldd", "--version").CombinedOutput()
}

// detectLibcWith answers the libc question, running the `ldd` probe ONLY when the
// loader glob cannot answer on its own.
//
// The order is the behaviour, not a tidy-up. With the marker present the
// reference reports musl and starts no `ldd` process; with the marker masked it
// runs `ldd`. The ordering is INFERRED from that pair, not assumed — what was
// observed is which process got spawned.
//
// claustrum ran ldd unconditionally and only then consulted the glob, so on that
// path it executed a PATH-RESOLVED BINARY THE REFERENCE DOES NOT EXECUTE THERE,
// and paid the probe timeout for an answer it was going to discard. That matters
// more here than elsewhere because `-install` is the one mode with a
// network-facing threat model.
//
// Measured 2026-08-02 against 5db5e4a, with a stand-in `ldd` on PATH that records
// its own invocation:
//
//	musl loader present   reference: ldd NOT run, libc=musl
//	                      claustrum: ldd RAN,     libc=musl
//	loader masked (ctl)   both: ldd RAN, libc=glibc
//
// The control is what makes the first row mean something: with the marker hidden
// both binaries reach the stand-in, so "not run" is an observation and not a
// broken fixture.
//
// glob is injectable for the same reason classifyLibc takes one — the musl branch
// is otherwise unreachable on a glibc host.
// The timeout and runner are injected so the timeout/fallback path is exercisable
// on any host (mirroring classifyLibc's injectable glob).
func detectLibcWith(timeout time.Duration, run func(context.Context) ([]byte, error),
	glob func(string) ([]string, error)) string {
	if hasMuslLoader(glob) {
		return "musl" // run() is deliberately never called on this path
	}
	ctx, cancel := lddCtx(timeout)
	defer cancel()
	out, err := run(ctx)
	return classifyLibc(out, err, glob)
}

// lddCtx builds the context for the `ldd` probe, arming a deadline only when one is
// asked for. At the shipped default (0) it returns a plain Background context, so
// nothing is armed and exec.CommandContext has no cancel path — NOT a huge-but-finite
// deadline. Do not "simplify" the two into one: a context with a far-off deadline
// still reports one, still spawns the timer goroutine, and still kills the probe
// eventually, which is the divergence this flip removes. Mirrors gitCtx (D5).
func lddCtx(timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.Background(), func() {}
	}
	return context.WithTimeout(context.Background(), timeout)
}

// muslLoaderGlob matches the musl dynamic loader for ANY architecture. The
// reference carries this glob; claustrum used to stat a hardcoded
// "/lib/ld-musl-x86_64.so.1", which cannot see the loader on arm64 or riscv.
const muslLoaderGlob = "/lib/ld-musl-*.so.*"

// hasMuslLoader reports whether the musl dynamic loader is present. Shared by
// detectLibcWith (which uses it to decide whether ldd is spawned AT ALL) and
// classifyLibc (which uses it to decide the reported value), so the two are
// definitionally the same predicate.
//
// They must not drift: the argument that this whole ordering change is
// wire-invisible rests on the two agreeing, so an edit to one — a second loader
// path, a match rule stronger than "any match" — would silently change WHEN ldd
// runs relative to HOW the answer is computed. One definition removes that
// possibility.
//
// It does NOT reduce the number of glob calls: on a glibc host detectLibcWith
// asks, gets no match, runs ldd, and classifyLibc asks again — two
// filepath.Glob calls, exactly as before. What was deduplicated is the source,
// not the call. Said plainly because the cost is unmeasurable today and a reader
// who trusts a "runs once" claim will not check, which matters the moment
// something more expensive than a glob sits behind this predicate.
//
// A glob error is treated as "no loader", which is the same fallback both call
// sites had: the ldd path still decides.
func hasMuslLoader(glob func(string) ([]string, error)) bool {
	m, err := glob(muslLoaderGlob)
	return err == nil && len(m) > 0
}

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
	if hasMuslLoader(glob) {
		return "musl"
	}
	if lddErr == nil && strings.Contains(strings.ToLower(string(lddOut)), "musl") {
		return "musl"
	}
	return "glibc"
}
