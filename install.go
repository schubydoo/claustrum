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
	"sync/atomic"
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
	// Fetch is the download stats object 4534d86 appends LAST, whenever a -cli-url
	// download was attempted (even a 0-byte 404). omitempty (a pointer) drops it on
	// the -cli-zst / cache-hit / no-source paths, where the reference emits no fetch.
	Fetch *fetchStats `json:"fetch,omitempty"`
}

func runInstall(o installOpts) {
	// Reset the -cli-url download sink; fetchToFile fills it when a download is
	// attempted, and it is read back into f.Fetch below (4534d86).
	lastInstallFetch = nil
	lastInstallFinal = nil
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
			sweepFetchTemps(o.cliDir, time.Now())
			if err == nil && o.cliKeep > 0 {
				pruneCLI(o.cliDir, o.cliKeep)
			}
		}
	}

	// The reference appends the fetch object whenever a -cli-url download ran (set by
	// fetchToFile), including a cache hit that still downloaded is impossible — a
	// cache hit skips ensureCLI entirely, so lastInstallFetch stays nil there.
	f.Fetch = lastInstallFetch
	b, _ := json.Marshal(f)
	fmt.Printf("__INSTALL_RESULT__%s\n", b)
}

// hashBlobFile is sha256File behind a seam, so ensureCLI's -cli-zst hash-failure arm is
// reachable from a test: the blob has just been opened and read a byte at that
// point, so no fixture can make the very next hash of the same path fail.
// Production never reassigns it.
var hashBlobFile = sha256File

// ensureCLI materializes the CLI binary from a .zst blob: either an already
// uploaded one (-cli-zst, SFTP fallback) or a download (-cli-url), verified
// against -cli-checksum, then zstd-decompressed to cliPath.
func ensureCLI(o installOpts, cliPath string) error {
	// Validate -cli-version BEFORE anything touches the filesystem. The rules and
	// their measurements live on validateCLIVersion; both are claustrum-only
	// hardening.
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
	// On every cache miss the cli-dir is created FIRST, before the source check,
	// before the blob is opened and before any network access. The reference
	// creates the cli-dir (0700, with its parents) there. A Linux VM measured it
	// on -cli-zst from 5db5e4a to f6010b97. It measured -cli-url and a miss with
	// no source flag on f6010b97. A failure leaves the cli-dir, empty if it was
	// new. A missing blob, a 404, a refused connection and a missing source all
	// leave it. A -cli-url download also shows it 1.5 s into a 3 s wait for
	// headers.
	//
	// 0700, not 0755: the reference creates the whole cli-dir chain owner-only.
	// Probe-measured under umask 022 against 5db5e4a. The "mkdir cli dir: "
	// prefix is measured against 5db5e4a too, and it lands in cliError.
	if err := os.MkdirAll(filepath.Dir(cliPath), 0o700); err != nil {
		return fmt.Errorf("mkdir cli dir: %v", err)
	}
	switch {
	case o.cliZst != "":
		// SFTP fallback. If the caller supplies a -cli-checksum, the reference checks the blob
		// against it since 7d193f89. 5db5e4a ignored it. A mismatch answers
		// "checksum mismatch" and keeps the blob. An absent or empty checksum
		// verifies nothing. claustrum hashes before it decompresses, which is
		// the D13 ordering, so a corrupt blob with a wrong checksum answers
		// "checksum mismatch" here where the reference answers "decompressing: ".
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
			if blobSum, err = hashBlobFile(blobPath); err != nil {
				return fmt.Errorf("opening input: %v", err)
			}
			if err := verifyChecksum(blobSum, o.cliChecksum); err != nil {
				return err
			}
		}
	case o.cliURL != "":
		// The temp lands in the cli-dir, created above, when it can. See
		// fetchToFile for why a failure there falls back rather than erroring.
		blobPath, blobSum, err = fetchToFile(o.cliURL, filepath.Dir(cliPath))
		if err != nil {
			// A status failure and a read-idle stall are already fully worded and go
			// out bare; every other download error (a transport failure, the D10 cap,
			// the D12 deadline, a disk error) takes the "download failed: " prefix. The
			// reference's cliError is "download failed with status N" for a non-200 and
			// the bare "download stalled: …" for a stall — prefixing the stall would
			// emit "download failed: download stalled: …", a wire divergence on
			// __INSTALL_RESULT__ (see httpStatusError, stalledError).
			var se *httpStatusError
			if errors.As(err, &se) {
				return se
			}
			var st *stalledError
			if errors.As(err, &st) {
				return err
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
		// The final progress line comes only after the checksum passes. Measured
		// on a Linux VM against f6010b97, 5 of 5 runs per row: a good download
		// prints bytes:0 then bytes:57, and a mismatch prints bytes:0 only.
		emitFinalProgress()
	default:
		return fmt.Errorf("cli %s missing and no --cli-url or --cli-zst provided", o.cliVersion)
	}
	// Stage, verify and install, retried ONCE if a concurrent install's sweep
	// reclaimed our staging file. See stageAndInstall for when that can happen.
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
// does. Since the sweep's age gate (sweepMinAge), a concurrent install sweeps
// ours only once it is more than ten minutes old, for example behind a CLI whose
// --version takes that long.
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
// The caller retries once on errStagingVanished, which covers that case. The
// age gate is the reference's own rule, not a guard for this: a staging file
// older than the gate is indistinguishable from litter by name and age alone.
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
// out. Two rules, both claustrum-only.
//
//  1. A SINGLE PATH COMPONENT (D6). cliPath is filepath.Join(cliDir, cliVersion)
//     and ensureCLI's os.RemoveAll deletes cliPath recursively, so a version that
//     reaches outside cliDir destroys unrelated data. Measured, the reference
//     destroys the target on "../victim". "../victim" escapes because Join
//     CLEANS; "link/1.0.0" escapes through a symlink under cliDir, which a
//     lexical containment check accepts because it is lexically inside.
//
//  2. NOT THE DOWNLOAD BLOB PREFIX. See isDownloadBlobName below.
//
// A version that the sweep claims (".fetch-x", "1.0.zst") is ACCEPTED, as the
// reference accepts it. Since 4534d86 the sweep removes only entries more than
// ten minutes old, so such a version installs and stays until a later install
// finds it that old. Measured on a Linux VM against 4534d86 through f6010b97.
// claustrum refused these versions until then (the retired D7).
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
	if isDownloadBlobName(v) {
		// A version with claustrum's own blob prefix installs fine and is then
		// exempt from pruneCLI's census FOREVER. It is never counted against
		// -cli-keep, never evicted, and never swept, because neither pass claims
		// that prefix. This is D18.
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
// calls it unconditionally; the -cli-zst path only when a checksum is supplied,
// as the reference does.
//
// The compare is CASE-SENSITIVE. got is lower-case hex, so an upper-case
// checksum fails even when it names the right digest. Measured on a Linux VM on
// the -cli-zst path against 7d193f89 through f6010b97: the reference answers
// `checksum mismatch: expected=5739E4…, actual=5739e4…` there. claustrum used
// strings.EqualFold and installed. Both source paths share this function. The
// -cli-url path is measured case-sensitive on f6010b97 too.
//
// It takes the hex digest rather than the bytes so no caller has to hold the
// whole blob in memory to check it: the download hashes as it streams, and the
// local path hashes the file in one bounded pass (sha256File).
func verifyChecksum(got, expected string) error {
	if got != expected {
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

// probeCLIVerdict classifies the outcome of the -probe-cli bounded runnability
// probe (reference build 19f30c46). The reference's -probe-cli mode reports exactly
// these three states.
type probeCLIVerdict int

const (
	probeCLIRuns probeCLIVerdict = iota // `<path> --version` exited 0 within the deadline
	probeCLIHung                        // the deadline fired and the process had to be killed
	probeCLIBad                         // missing, failed to start, or exited non-zero
)

// probeCLITimeout is the fixed wall-clock bound the -probe-cli mode puts on the
// `<cli> --version` probe. The reference value is not probe-measured. It is NOT
// the opt-in -cli-probe-timeout (D11) divergence: -probe-cli is a standalone mode
// Claude Desktop drives to classify a CLI binary, and the reference always bounds it.
// It is a var, not a const, only so tests can shrink it (same idiom as
// stdinQueueCap in process.go).
var probeCLITimeout = 30 * time.Second

// probeCLIKillGrace is the WaitDelay the -probe-cli mode puts on the probe. Once the
// deadline tears down the process group, the runtime waits at most this long for the
// command's I/O to drain before it force-closes, so a descendant that inherited the
// probe's stdout cannot keep the mode blocked past it. Fixed, like probeCLITimeout.
const probeCLIKillGrace = 2 * time.Second

// probeCLIRunnable runs `<path> --version` under probeCLITimeout and classifies the
// outcome the way the reference's -probe-cli mode does: it exited 0 (runs), the
// deadline had to kill it (hung), or it is missing / failed to start / exited
// non-zero (bad). Unlike isRunnable, -probe-cli always bounds the probe, so this
// always creates the context.
//
// The probe runs in its OWN process group (newSysProcAttr) and the deadline tears
// down the WHOLE group, not just the direct child, so a `--version` that forks a
// descendant (a wrapper shell, a helper) cannot outlive the probe. A plain
// CommandContext would SIGKILL only the direct child and leak the rest — the
// reparented-sleeper leak slowCLI's comment measured.
//
// A consequence of the own-group isolation: a terminal Ctrl-C signals only the
// foreground group (the claustrum process), NOT this child, so an interrupted probe
// leaves the CLI to the deadline or to exit on its own rather than dying with the
// Ctrl-C. That is intentional parity: SIGINT ends the reference's -probe-cli with
// exit 130 and empty stdout (19f30c46). Do NOT add a SIGINT reaper here: it would
// diverge.
func probeCLIRunnable(path string) probeCLIVerdict {
	ctx, cancel := context.WithTimeout(context.Background(), probeCLITimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.SysProcAttr = newSysProcAttr()
	cmd.Cancel = func() error {
		// Deadline fired: SIGKILL the child's whole process group (kill(-pgid) on
		// unix; a no-op reap on Windows, where the direct kill below plus WaitDelay
		// bound it), then kill the direct child so it dies at once on every OS.
		reapProcessGroup(cmd.Process)
		_ = cmd.Process.Kill()
		return nil
	}
	cmd.WaitDelay = probeCLIKillGrace
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return probeCLIHung
		}
		return probeCLIBad
	}
	return probeCLIRuns
}

// writeProbeCLIResult writes the -probe-cli mode's stdout for a verdict: nothing
// when the CLI runs, "__CLI_HUNG__\n" when the deadline killed it, "__CLI_BAD__\n"
// when it is missing or does not run (reference build 19f30c46: the reference emits
// the token followed by a single trailing newline, and emits nothing at all on
// success — measured byte-for-byte). The mode always exits 0 regardless.
func writeProbeCLIResult(w io.Writer, v probeCLIVerdict) {
	switch v {
	case probeCLIHung:
		fmt.Fprintln(w, "__CLI_HUNG__")
	case probeCLIBad:
		fmt.Fprintln(w, "__CLI_BAD__")
	case probeCLIRuns:
		// The CLI ran: print nothing.
	}
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

// zstdDecompress decompresses the zstd blob at src to dest in-process, using
// klauspost/compress. It needs no external zstd CLI.
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
// them differently — the status form is bare, while a transport failure takes the
// "download failed: " prefix (as does every other non-status, non-stall error).
//
//	transport : download failed: Get "http://…": dial tcp …: connection refused
//	status    : download failed with status 404
type httpStatusError struct{ code int }

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("download failed with status %d", e.code)
}

// cliDownloadTimeout bounds the whole `-cli-url` exchange in fetchToFile.
//
// ZERO (the default) DISABLES IT, which is the parity position for the TOTAL-exchange
// deadline: no total cap was observed on the reference within the window measured. A
// fully STALLED body (no bytes at all) is handled
// separately by the always-on read-idle abort newWatchedBody adds (installIdleTimeout,
// 60 s — 4534d86 parity, VM-measured), NOT by this bound. What this bound would add
// beyond that is a cap on a slow-but-PROGRESSING download, and no total cap was
// observed on the reference within the window measured: VM-measured against 4534d86, a
// body trickling 1 byte every 30 s was still downloading at 150 s (each byte resets the
// read-idle clock, so the read-idle abort never fires), so a total deadline below an
// honest slow download's duration fails a transfer the reference does not bound in that
// window. (The earlier 5db5e4a measurement — still downloading a never-sent body at
// 400 s — predates the read-idle abort; on 4534d86 that same never-sent body aborts at
// 60 s via the read-idle path, not this deadline.)
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

// closeFetchTemp is (*os.File).Close behind a seam over the one close whose error
// fetchToFile checks (the temp file's; the response body and the watched body are
// closed unchecked). That close fails only on a deferred write-back error (a full or
// failing filesystem surfacing at close after every Write returned nil), which no
// fixture can provoke portably — so without the seam fetchToFile's closeErr arm,
// which must still remove the half-written temp, is unreachable. Production never
// reassigns it.
var closeFetchTemp = (*os.File).Close

// fetchToFile downloads url to a temporary file and returns that file's path
// together with the sha256 computed WHILE streaming, so the blob is never held in
// memory and is never hashed in a second pass. The caller owns the temp file and
// must remove it.
//
// The temp is preferentially created in dir, the cli-dir, which puts it on the
// filesystem the install writes to. ensureCLI creates the cli-dir before it calls
// this. If the create still fails, for example in an unwritable cli-dir, the temp
// falls back to the OS temp dir rather than reporting a download failure.
func fetchToFile(url, dir string) (path, sum string, err error) {
	// start times the whole exchange so fetch.ms matches the reference (a 404 reports
	// ms of the request round-trip). A pre-body failure records ms with zero bytes;
	// the watched body overwrites lastInstallFetch with real stats once the download
	// begins. Set on every -cli-url path so runInstall always sees a fetch object.
	start := time.Now()
	client := &http.Client{Timeout: cliDownloadTimeout}
	resp, err := client.Get(url)
	if err != nil {
		lastInstallFetch = &fetchStats{Ms: time.Since(start).Milliseconds()}
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		lastInstallFetch = &fetchStats{Ms: time.Since(start).Milliseconds()}
		// "download failed with status %d" — the reference's exact wording, and it
		// carries neither the URL nor the reason phrase. Measured at 5db5e4a: a 404
		// gives cliError "download failed with status 404" where claustrum emitted
		// "download failed: download <url>: 404 File not found". The URL is worth
		// omitting on its own merits: cliError is printed on the __INSTALL_RESULT__
		// line, and a signed URL would land in whatever captures that output.
		return "", "", &httpStatusError{code: resp.StatusCode}
	}
	// ⚠️ The prefix must be one isSweptName does NOT claim. sweepFetchTemps runs
	// after EVERY attempted install and takes old ".fetch-*" and "*.zst" from the
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
	f, err := os.CreateTemp(dir, blobTempPrefix+"*")
	if err != nil {
		if f, err = os.CreateTemp("", "claustrum-fetch-*"); err != nil {
			lastInstallFetch = &fetchStats{Ms: time.Since(start).Milliseconds()}
			return "", "", err
		}
	}
	tmp := f.Name()
	h := sha256.New()
	// Wrap the body so the download emits __INSTALL_PROGRESS__ ticks, aborts on a
	// read-idle stall, and records the fetch stats — the 4534d86 instrumentation.
	// total is the Content-Length (0 when the server sends none, which drops the
	// progress `total` field).
	total := resp.ContentLength
	if total < 0 {
		total = 0
	}
	wb := newWatchedBody(resp.Body, total, start)
	// Same bypass as zstdDecompress: with the cap off the body streams straight
	// through, hashed on the way past.
	var n int64
	var copyErr error
	if maxCLIBytes <= 0 {
		n, copyErr = io.Copy(io.MultiWriter(f, h), wb)
	} else {
		n, copyErr = io.Copy(io.MultiWriter(f, h), io.LimitReader(wb, maxCLIBytes+1))
	}
	// Record fetch stats even on copyErr: the reference emits the fetch object on a
	// stall, not only on success.
	s := wb.stats()
	lastInstallFetch = &s
	_ = wb.Close() // stop the progress ticker
	closeErr := closeFetchTemp(f)
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
	lastInstallFinal = &progressLine{Phase: "download", Bytes: n, Total: total}
	return tmp, hex.EncodeToString(h.Sum(nil)), nil
}

// sweepMinAge is how old a swept name must be before the sweep removes it.
// The gate exists from 4534d86 on. 5db5e4a and 7d193f89 swept at every age.
// Measured on a Linux VM against 19f30c46, 90fca6e6 and f6010b97: an entry
// whose mtime is 599 s old stays, and one 601 s old goes (two runs each). The
// exact 600 s point is not measured, and claustrum treats it as not old.
const sweepMinAge = 10 * time.Minute

// sweepFetchTemps removes install litter from the cli-dir: an entry whose name
// isSweptName claims AND whose mtime is more than sweepMinAge before now. The
// litter is an interrupted install's ".fetch-<something>" or a stray "*.zst".
// claustrum's pruneCLI used to count such litter as CLI *versions*, so it
// consumed the -cli-keep budget and evicted real binaries.
//
// The rules, measured on the reference from 4534d86 on (the future-mtime row on
// 19f30c46 through f6010b97):
//   - The age is the entry's own mtime, for a symlink the link's own mtime.
//     atime and ctime are not read. Only the link is removed. A target outside
//     the cli-dir stays (the only case measured). os.Lstat, not DirEntry.Info:
//     on Windows Info comes from the directory listing, which can hold a stale
//     time for a directory.
//   - A future mtime is not old, so it stays.
//   - os.Remove per entry. It removes files and EMPTY directories and never
//     recurses, so a non-empty ".fetch-dir/" stays at every age.
//
// A concurrent install's staging file is fresh, so the age gate now keeps it.
// stageAndInstall's retry still covers a staging file that outlives the gate.
func sweepFetchTemps(cliDir string, now time.Time) {
	ents, err := os.ReadDir(cliDir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !isSweptName(e.Name()) {
			continue
		}
		fi, err := os.Lstat(filepath.Join(cliDir, e.Name()))
		if err != nil || now.Sub(fi.ModTime()) <= sweepMinAge {
			continue
		}
		_ = os.Remove(filepath.Join(cliDir, e.Name()))
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
// the same rule. A rule the validator does not consult is one an operator can
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
// The match is case-sensitive, and the bare names ".fetch-" and ".zst" count.
// ".FETCH-u" and "a.ZST" stay. Measured on every reference build from 5db5e4a
// to f6010b97.
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
		// A name the sweep claims is not counted either, at ANY age. Measured on a
		// Linux VM against f6010b97: with a fresh ".fetch-o" and "x.zst" beside
		// three real CLIs and -cli-keep 3, the reference keeps all three real CLIs.
		// The age gate leaves such fresh litter in place, so counting it evicts
		// real CLIs. The fixture does not show whether the reference tests the
		// name or runnability. Both exclude these two empty files.
		if isSweptName(e.Name()) {
			continue
		}
		// A concurrent install's in-flight download blob is not a CLI version.
		// Counted, it sorts NEWEST, takes a -cli-keep slot and evicts a real
		// binary.
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

// lddProbeTimeout bounds how long `ldd --version` itself runs. It is a var,
// not a const, only so tests can shrink it. Nothing else sets it.
//
// Measured on a Linux VM with a stub `ldd` first on PATH. From 19f30c46 on, the
// reference cuts the probe at 5 s: a 4.9 s stall answers at 4.93 s, and a 5.1 s
// stall answers at 5.01 to 5.04 s. 3ef9370 and older builds wait for ldd, with no
// bound. The bound fires on a fresh install and on a cache hit alike.
//
// The bound covers the ldd process only. If ldd exits in time, the drain that
// follows (lddKillGrace) can end past 5 s.
var lddProbeTimeout = 5 * time.Second

// lddKillGrace is the WaitDelay on the probe. It caps the wait for ldd's output
// pipe to close after ldd itself exits. The output already read is then USED,
// and the process that still holds the pipe is not killed. Measured on a Linux
// VM against f6010b97: an ldd that prints a musl banner, leaves a `sleep` child
// holding its stdout and exits at 0.5 to 4.5 s answers musl about 2 s after it
// exits on the reference. The child survives.
const lddKillGrace = 2 * time.Second

// errLddBound marks an ldd run that the bound killed while ldd itself still ran.
// It wraps context.DeadlineExceeded, which is what detectLibcWith tests for.
var errLddBound = fmt.Errorf("ldd killed at the bound: %w", context.DeadlineExceeded)

// runLddVersion runs `ldd --version` under ctx. ldd runs in its OWN process
// group. If ctx expires while ldd runs, the whole group is killed, not only ldd,
// and the run reports errLddBound. Measured: the reference runs the stub in its
// own group (pgid = the stub's pid), and a `sleep` child of the stub is gone
// right after the reference answers at 5.02 s.
//
// Cancel runs only if ctx expires before Wait sees ldd exit. After ldd exits,
// WaitDelay alone bounds the drain.
func runLddVersion(ctx context.Context) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ldd", "--version")
	cmd.SysProcAttr = newSysProcAttr()
	var killed atomic.Bool
	cmd.Cancel = func() error {
		killed.Store(true)
		reapProcessGroup(cmd.Process)
		_ = cmd.Process.Kill()
		return nil
	}
	cmd.WaitDelay = lddKillGrace
	out, err := cmd.CombinedOutput()
	if killed.Load() {
		return out, errLddBound
	}
	return out, err
}

// detectLibcWith answers the libc question by running the `ldd` probe FIRST and
// consulting the musl loader glob only as a fallback. classifyLibc holds the
// precedence and the reference measurement.
//
// The order is the behaviour, not a tidy-up. Reference build 3ef9370 runs `ldd`
// on every call and lets its output decide; the loader glob is reached only when
// ldd produced nothing. Earlier builds (4534d86 and before) did the reverse,
// glob first and ldd only on a miss. claustrum matched that until this build.
// Measured against both reference binaries on a mixed host (glibc ldd plus a musl
// marker): 4534d86 reports musl, 3ef9370 reports glibc.
//
// When the bound kills ldd, the output is DROPPED, even output ldd wrote before
// it stalled, so the loader glob decides. Measured on f6010b97 and 90fca6e6: a
// stub that prints a musl banner and then stalls reports glibc at 5.04 s with no
// marker. No log line, no stderr, and the facts line keeps its normal shape. An
// ldd that exited in time keeps its output, even when the drain ends past 5 s.
//
// The runner and timeout are injected so the bound is exercisable on any host,
// and glob is injectable for the same reason classifyLibc takes one: the musl
// fallback is otherwise unreachable on a glibc host. A runner reports a kill at
// the bound with an error that wraps context.DeadlineExceeded.
func detectLibcWith(timeout time.Duration, run func(context.Context) ([]byte, error),
	glob func(string) ([]string, error)) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := run(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		out = nil
	}
	return classifyLibc(out, glob)
}

// muslLoaderGlob matches the musl dynamic loader for ANY architecture. claustrum
// used to stat a hardcoded "/lib/ld-musl-x86_64.so.1", which cannot see the
// loader on arm64 or riscv.
const muslLoaderGlob = "/lib/ld-musl-*.so.*"

// hasMuslLoader reports whether the musl dynamic loader is present. Since the
// reorder (build 3ef9370) classifyLibc is its only caller, and only on the
// empty-output fallback — when ldd produced nothing, the loader glob decides the
// reported value. detectLibcWith no longer consults it before running ldd; ldd
// runs unconditionally now, so this predicate no longer gates whether ldd spawns.
//
// A glob error is treated as "no loader", the fallback the call site relies on:
// with no loader match the empty-output path reports glibc.
func hasMuslLoader(glob func(string) ([]string, error)) bool {
	m, err := glob(muslLoaderGlob)
	return err == nil && len(m) > 0
}

// classifyLibc maps an `ldd --version` result to "musl" or "glibc". It is split
// from detectLibc with an injectable glob so both branches are testable on any
// host — a glibc box can't otherwise reach the musl fallback.
//
// The order matches reference build 3ef9370, which reordered this in the daemon:
//  1. If the `ldd` output contains "musl" (case-insensitive), report musl. The
//     ldd exit code is NOT consulted — a faithful musl `ldd --version` prints its
//     banner to stderr and exits 1, and CombinedOutput captures that stderr, so
//     the banner still decides. Measured against 3ef9370 in a glob-miss
//     environment (no /lib/ld-musl-* marker, so no fallback can rescue the answer):
//     a stub ldd printing a musl banner and exiting 1 reports musl. With the marker
//     absent the banner alone produces musl, so a non-zero exit does not gate step 1.
//  2. Otherwise, if `ldd` produced any output, report glibc. The loader glob is
//     NOT consulted on this path, and the exit code is not consulted here either.
//     This needs the opposite environment to step 1 to discriminate: measured
//     against 3ef9370 on a marker-PRESENT host, a stub printing glibc output and
//     exiting 1 reports glibc. If a non-zero exit dropped to the glob, the present
//     marker would force musl; it reports glibc, so the exit is ignored here too.
//  3. Only when `ldd` produced no output at all does the musl loader glob decide:
//     present → musl, else glibc. This is the missing-ldd path, and the path of
//     an ldd the bound killed, because detectLibcWith drops its output.
//
// This reverses the ordering claustrum carried for build 4534d86 and earlier,
// where the loader glob was consulted FIRST and outranked ldd. The mixed host is
// where the two orderings part: a glibc box whose `ldd` succeeds and reports
// glibc while a `/lib/ld-musl-*.so.*` marker is also installed. 4534d86 reported
// musl there; 3ef9370 reports glibc, because ldd produced non-musl output and the
// glob is never reached. Measured on both reference binaries on one such host.
//
// Clean single-libc hosts report the same value under either ordering: a Debian
// box has no musl marker and its ldd says glibc; an Alpine box has a musl marker
// and its ldd prints a musl banner, so step 1 catches it before the glob matters.
func classifyLibc(lddOut []byte, glob func(string) ([]string, error)) string {
	if strings.Contains(strings.ToLower(string(lddOut)), "musl") {
		return "musl"
	}
	if len(lddOut) != 0 {
		return "glibc"
	}
	if hasMuslLoader(glob) {
		return "musl"
	}
	return "glibc"
}
