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
	"sync"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/zstd"
)

type installOpts struct {
	cliDir, cliVersion, cliURL, cliChecksum, cliZst string
	cliKeep                                         int
	// home is the user's home folder. runInstall uses it only for the default
	// cli folder, when -cli-version is given and -cli-dir is absent or empty.
	home string
}

// defaultCLIDir is the cli folder of an -install with -cli-version and no
// -cli-dir, or an empty one: <home>/.claude/remote/ccd-cli. Measured on 89cb6289
// (rows H01, H03, H04 and H05, each on Linux, macOS and Windows). On Windows
// <home> is USERPROFILE, and HOME is not read (row H10).
func defaultCLIDir(home string) string {
	return filepath.Join(home, ".claude", "remote", "ccd-cli")
}

// installCLIPath is the final path of the CLI file. On Windows the file name is
// <version>.exe, always, also for a version that ends in ".exe" already (rows
// W01 and W06, Windows VM, 89cb6289). cliExeSuffix is empty on the other systems.
func installCLIPath(cliDir, cliVersion string) string {
	return filepath.Join(cliDir, cliVersion+cliExeSuffix)
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
	// cliUnresponsive comes right after cliError. It is true for a --version run
	// that its bound stopped (89cb6289). A direct run is measured on Linux, macOS
	// and Windows. A launcher run is measured on Linux and macOS.
	//
	// The managed launcher fields come after it, only when
	// CLAUDE_SSH_MANAGED_LAUNCHER=1. The order was measured across every row:
	// cliUnresponsive, launcherStatus, launcher, launcherSource, launcherPath,
	// launcherReason, launcherStderr. A field that does not apply is omitted.
	CliUnresponsive bool `json:"cliUnresponsive,omitempty"`
	// Fetch is the download stats object of 4534d86. It is present whenever a
	// -cli-url download was attempted (even a 0-byte 404). omitempty (a pointer)
	// drops it on the -cli-zst / cache-hit / no-source paths, where the reference
	// emits no fetch. Without the gate it is the last field. With the gate it comes
	// after cliUnresponsive and before launcherStatus (measured on a Linux VM for
	// the statuses usable, none, unusable, probe_failed and unresponsive).
	Fetch          *fetchStats `json:"fetch,omitempty"`
	LauncherStatus string      `json:"launcherStatus,omitempty"`
	Launcher       []string    `json:"launcher,omitempty"`
	LauncherSource string      `json:"launcherSource,omitempty"`
	LauncherPath   string      `json:"launcherPath,omitempty"`
	LauncherReason string      `json:"launcherReason,omitempty"`
	LauncherStderr string      `json:"launcherStderr,omitempty"`
}

func runInstall(o installOpts) {
	// Reset the -cli-url download sink; fetchToFile fills it when a download is
	// attempted, and it is read back into f.Fetch below (4534d86).
	lastInstallFetch = nil
	lastInstallFinal = nil
	installSwept = false
	// installManaged lives for this run only, so a later direct ensureCLI call
	// (tests) never sees a stale launcher.
	installManaged = nil
	defer func() { installManaged = nil }()
	f := installFacts{
		ServerVersion: Version,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		Libc:          detectLibc(),
	}

	// Without -cli-version there is no CLI path, and the default folder does not
	// apply (row H06, macOS and Windows).
	if o.cliVersion != "" {
		if o.cliDir == "" && o.home != "" {
			o.cliDir = defaultCLIDir(o.home)
		}
		if o.cliDir != "" {
			f.CliPath = installCLIPath(o.cliDir, o.cliVersion)
		}
	}
	if f.CliPath != "" {
		// With CLAUDE_SSH_MANAGED_LAUNCHER=1 the launcher is resolved for the CLI path
		// before the CLI runs, and installCLICheck runs the CLI through it. With no
		// CLI path the facts line gains no launcher field (measured without
		// -cli-version, Linux VM).
		if managedLauncherGateOn() {
			installManaged = &installManagedState{res: resolveManagedLauncher(f.CliPath)}
		}

		// An old "*.zst.part" entry goes before the CLI runs, on every path, also
		// on a cache hit (cell Z4b, Linux VM, 89cb6289).
		sweepZstParts(o.cliDir, time.Now())

		// "present" requires the file to exist AND be runnable (real binary checks
		// `<cli> --version`). A freshly downloaded CLI leaves cliWasPresent false.
		// On Windows only <version>.exe counts. A file with the bare name counts
		// for nothing (rows W02 to W05).
		checkErr := errCLINotRunnable
		if isRegularFile(f.CliPath) {
			checkErr = installCLICheck(f.CliPath, false)
		}
		var stopped *cliStoppedError
		if errors.As(checkErr, &stopped) {
			// A run of a present CLI that its bound stopped, direct or through a
			// launcher: cliWasPresent false, the cliError text and cliUnresponsive.
			// The CLI file stays, the sweep runs and nothing is pruned (89cb6289,
			// rows B04 to B11, B15, L03, L05). No source flag is used: rows B10 and
			// B11 for a direct run. With a launcher that is not measured.
			f.CliError = checkErr.Error()
			f.CliUnresponsive = true
			sweepFetchTemps(o.cliDir, time.Now())
		} else if checkErr == nil {
			// Cache hit with a good run: neither the orphan sweep nor the prune
			// runs here, as on the reference. A stopped run is the branch above,
			// and it sweeps. The "*.zst.part" sweep ran above.
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
				f.CliUnresponsive = errors.As(err, &stopped)
				// A cli folder that cannot be made answers an empty cliPath
				// (row E10, Linux VM, 89cb6289).
				// The reference prints no launcher field beside that empty
				// cliPath, with the gate set (cell G1, Linux VM).
				var mk *cliDirError
				if errors.As(err, &mk) {
					f.CliPath = ""
					installManaged = nil
				}
			}
			// The sweep runs once per attempted install, whether or not the
			// install succeeded. The prune runs only when it succeeded.
			//
			//	scenario                       sweep            prune
			//	cache hit, good run            no               no
			//	cache hit, stopped run         yes              no
			//	install fails before the run   yes              no
			//	install, good run              before the run   yes
			//	install, stopped run           before the run   no
			//	install, new CLI does not run  before the run   no
			//
			// stageAndInstall sweeps before the run of a new CLI and sets
			// installSwept. Every other path sweeps here. The "*.zst.part"
			// sweep is not in this table: it ran above, on every path.
			if !installSwept {
				sweepFetchTemps(o.cliDir, time.Now())
			}
			if err == nil {
				// A negative keep value ends the run where the prune starts
				// (cells Kneg, -cli-keep -1, Linux, macOS and Windows VMs). On
				// 89cb6289 the new CLI is in place, the -cli-zst blob is gone
				// and nothing is pruned. The exit code is 2, stdout has no
				// result line, and stderr has a Go runtime error. claustrum
				// matches the exit code and the missing result line. The one
				// stderr line is claustrum's own (the maintainer's decision of
				// 2026-10-07).
				//
				// Not measured: a negative keep on a cache hit, after a failed
				// install and after a stopped run. No prune runs there, so
				// claustrum prints the result line and exits 0, as before.
				if o.cliKeep < 0 {
					fmt.Fprintf(os.Stderr, "claustrum: -cli-keep %d is not a valid keep count\n", o.cliKeep)
					osExit(2)
				}
				// A keep of 0 prunes too (cells K0a and K0b, Linux, macOS and
				// Windows VMs, 89cb6289).
				pruneCLI(o.cliDir, o.cliKeep)
			}
		}
	}

	// The reference appends the fetch object whenever a -cli-url download ran (set by
	// fetchToFile), including a cache hit that still downloaded is impossible — a
	// cache hit skips ensureCLI entirely, so lastInstallFetch stays nil there.
	f.Fetch = lastInstallFetch
	if installManaged != nil {
		installManaged.fillFacts(&f)
	}
	b, _ := json.Marshal(f)
	fmt.Printf("__INSTALL_RESULT__%s\n", b)
}

// installSwept records that stageAndInstall ran the sweep of this -install run
// already, before the --version run of a new CLI. runInstall resets it and then
// sweeps only when it is false, so one run sweeps once. A second sweep after a
// long --version run removes an entry that passed the age gate during the run.
var installSwept bool

// cliDirError is a cli folder that cannot be made. Its text lands in cliError,
// and runInstall answers an empty cliPath for it.
type cliDirError struct{ err error }

func (e *cliDirError) Error() string { return fmt.Sprintf("mkdir cli dir: %v", e.err) }

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
	var blobIsTemp bool
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
	//
	// A cli-dir entry that is not a folder is replaced first (clearNonFolderCLIDir).
	// It runs only for a cliPath that runInstall made from the cli-dir, so the
	// path it works on is the cli-dir itself.
	if o.cliDir != "" {
		clearNonFolderCLIDir(filepath.Dir(cliPath))
	}
	if err := os.MkdirAll(filepath.Dir(cliPath), 0o700); err != nil {
		return &cliDirError{err: err}
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
			// A body read that ended with a transport error is fully worded too
			// (row E07, see interruptedError).
			var it *interruptedError
			if errors.As(err, &it) {
				return err
			}
			return fmt.Errorf("download failed: %v", err)
		}
		// The download blob is claustrum's own temp file. stageAndInstall removes
		// it before the --version run of a new CLI. This defer covers every
		// other path.
		blobIsTemp = true
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
	consumeBlob, err := stageAndInstall(blobPath, cliPath, blobIsTemp)
	if err != nil && errors.Is(err, errStagingVanished) {
		// Accumulate rather than overwrite. The answer is about the blob, not
		// about an attempt: once one attempt says that the blob is consumed, the
		// consume rule below holds for good. With a plain assignment, a retry
		// that fails BEFORE its own decompress (a CreateTemp or write error)
		// answers false and keeps a blob that the first attempt had already
		// decompressed. That contradicts the rule stated right below.
		//
		// Not shown to be reachable: the sweep that triggers the retry removes
		// `.fetch-*`, not the cli-dir, so a second-pass CreateTemp failure needs
		// state the retry path does not itself produce. This is invariant
		// hygiene, and it costs one variable.
		retryConsume, retryErr := stageAndInstall(blobPath, cliPath, blobIsTemp)
		consumeBlob = consumeBlob || retryConsume
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
	// failure after it does not, with one exception: the home guard refusal
	// (D2) keeps the blob. (Not "before the staged file exists": os.CreateTemp
	// makes that file before zstdDecompress runs, so the bad-zstd row has a
	// staged file and still keeps the blob.) The cliError strings are
	// byte-identical on all four. The chmod failure and a failed clear sit on
	// the consumed side by construction, not by observation.
	if o.cliZst != "" && consumeBlob {
		_ = os.Remove(o.cliZst)
	}
	return err
}

// cliVersionMarker is the name of the one file beside the cli-dir that
// clearNonFolderCLIDir removes. The name is literal.
const cliVersionMarker = "ccd-cli-version"

// clearNonFolderCLIDir makes room for the cli folder when the cli-dir path names
// something that is not a folder. Measured against f6010b97 and 89cb6289, one
// run for each cell: E12x-a to E12x-n on Linux and macOS, 13 cells on Windows.
//
//   - The trigger is an existing entry of one of three kinds. They are a regular
//     file (cells a, b, g, h, j, l, m), a symlink to a regular file (d) and a
//     FIFO (k). The reference then removes that entry. For a symlink it removes
//     the link and keeps the target.
//   - It also removes the file named exactly "ccd-cli-version" in the parent
//     folder of the cli-dir. A file named other-version, ccd-cli-version.bak or
//     zzz-version stays (g, l).
//   - Then it makes the cli folder with mode 0700 and goes on. That happens with
//     no source flag too, before the "missing" answer (j).
//   - A folder (c), a symlink to a folder (e) and an absent entry (n) trigger
//     nothing. A dangling symlink (f) does not either. The version file stays.
//   - With a parent folder that is not writable nothing is removed, and the
//     answer is the mkdir error (i).
//
// Both deletes are a plain os.Remove of one path. Nothing here is recursive, and
// nothing follows a symlink to delete. Nothing here opens the FIFO: Lstat and
// Stat do not open it.
//
// Not measured: an entry of another type (a socket, a device, a symlink to a
// FIFO), and a "ccd-cli-version" that is a folder or a symlink. claustrum leaves
// these alone. Windows has no FIFO cell and no cell with a parent that is not
// writable. Not measured either: the order of the two deletes. claustrum removes
// the version file first and the entry second. When the entry stays, for example
// below a parent that is not writable, MkdirAll gives the mkdir error.
func clearNonFolderCLIDir(dir string) {
	li, err := os.Lstat(dir)
	if err != nil {
		return
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return // a dangling symlink (cell f)
	}
	if !fi.Mode().IsRegular() && li.Mode()&os.ModeNamedPipe == 0 {
		return
	}
	marker := filepath.Join(filepath.Dir(dir), cliVersionMarker)
	if mi, err := os.Lstat(marker); err == nil && mi.Mode().IsRegular() {
		if err := os.Remove(marker); err != nil {
			return
		}
	}
	_ = os.Remove(dir)
}

// errStagingVanished marks the one failure ensureCLI retries: the staging file
// was removed by another process between its creation and the rename.
var errStagingVanished = errors.New("staging file vanished")

// stageAndInstall decompresses the blob to a staging file beside cliPath, puts
// it at cliPath and then runs it. It follows the order of the reference,
// measured on 89cb6289:
//
//  1. What is at cliPath goes before the new CLI runs. A folder there is removed
//     as a tree, with the file in it (row E11 on Linux and macOS, row E11b on
//     Windows). A file there is replaced (row F14, Linux).
//  2. The new CLI is at cliPath. The sweep has run, and the download blob is
//     gone. The cli folder holds no `.fetch-` temp of this install (rows F01, F02
//     and W01, on Linux, macOS and Windows).
//  3. `<cliPath> --version` runs. The CLI and a managed launcher get the final
//     path (rows L06, L08 to L10).
//  4. A stopped run keeps the new CLI (rows F05 to F11, F14, F15, L07, L09). A
//     run that exits non-zero removes it and answers the "not runnable" text with
//     the final path (rows F12 and G05).
//
// The delete of step 1 is an os.RemoveAll of cliPath. The delete of step 4 is
// an os.Remove of the file that this call put there. ensureCLI runs
// validateCLIVersion (D6) before it calls this function, so cliPath is a direct
// child of the cli folder. D6 does not say which folder that child is, so
// cliFolderHoldsHome runs right before the delete of step 1. That guard is a
// divergence (D2): the reference removes a home folder there.
//
// Not measured: how the reference writes the final file, and the order of its
// sweep against that write. claustrum writes a staging file and renames it, so
// cliPath only ever holds a complete 0755 binary, and it sweeps after the rename.
// Not measured either: a new CLI that exits non-zero over a file or a folder at
// cliPath. claustrum then leaves nothing at cliPath, as for an empty one.
//
// Only a folder is removed in step 1. rename(2) replaces a file atomically, so a
// staging file that vanished costs an installed CLI nothing: the rename fails and
// the old file stays. A folder at cliPath is a stale blocker, not an install.
//
// The staging name is the reference's own temp name, ".fetch-<random>" in the
// cli-dir, so sweepFetchTemps reaps an interrupted install's litter. Mid-download
// the reference's cli-dir holds a ".fetch-<random>" of its own, with the
// decompressed CLI's first bytes (measured 2026-08-08, -cli-url only).
//
// A concurrent install's sweep can remove the staging file once it is more than
// ten minutes old (sweepMinAge). The loss shows at the rename, and the caller
// retries once on errStagingVanished. The exposed window is decompress and
// chmod. The download blob is outside isSweptName, so no sweep claims it.
//
// chmodStaged is os.Chmod behind a seam, so the one branch between decompress
// and rename that no fixture can otherwise provoke is reachable from a test.
// That branch matters more than its size: it is on the CONSUMED side of the
// blob rule, and until it was exercised the PR could only claim so by
// construction. Production never reassigns it.
var chmodStaged = os.Chmod

func stageAndInstall(blobPath, cliPath string, blobIsTemp bool) (consumeBlob bool, err error) {
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
	if fi, err := os.Lstat(cliPath); err == nil && fi.IsDir() {
		// The home guard (D2). A folder at cliPath that is the home folder, or
		// that contains it, is not removed. The reference removes it as a tree
		// (measured on Linux, macOS and Windows, see docs/DIVERGENCES.md D2). See
		// cliFolderHoldsHome. The folder is not removed here, and the blob is not
		// consumed: the answer is false, so ensureCLI keeps the -cli-zst blob.
		if cliFolderHoldsHome(cliPath, fi) {
			_ = os.Remove(tmp)
			return false, fmt.Errorf("cli path must not be or contain the home directory: %q", cliPath)
		}
		if rmErr := os.RemoveAll(cliPath); rmErr != nil {
			_ = os.Remove(tmp)
			return true, fmt.Errorf("clearing stale dir at %s: %v", cliPath, rmErr)
		}
	}
	if err := os.Rename(tmp, cliPath); err != nil {
		if _, statErr := os.Lstat(tmp); statErr != nil {
			// Our staging file is gone — a concurrent install's sweep took it.
			// A file at cliPath is deliberately left alone.
			return true, fmt.Errorf("%w before install: %v", errStagingVanished, err)
		}
		_ = os.Remove(tmp)
		return true, err
	}
	if blobIsTemp {
		_ = os.Remove(blobPath)
	}
	sweepFetchTemps(filepath.Dir(cliPath), time.Now())
	installSwept = true
	err = installCLICheck(cliPath, true)
	var stopped *cliStoppedError
	if err == nil || errors.As(err, &stopped) {
		return true, err
	}
	_ = os.Remove(cliPath)
	return true, fmt.Errorf("installed cli at %s is not runnable", cliPath)
}

// cliFolderHoldsHome is the home guard of the tree delete in stageAndInstall.
// fi is the Lstat answer of the folder at cliPath. Two tests run, and one yes
// refuses the delete:
//
//  1. wipesHomeDir, the lexical test of the RPC paths.
//  2. The folder is the home folder or one of its parent folders, by identity
//     (os.SameFile). The spelling of the two paths does not matter.
//
// Test 2 runs for the home path as given. It runs again for the path that
// finalDirPath gives, when that differs: the parents of a path through a link
// are not the parents of the folder it names.
//
// wipesHomeDir stays lexical because a destination that does not exist is legal
// on the RPC paths. Here the folder exists: its Lstat just succeeded.
func cliFolderHoldsHome(cliPath string, fi os.FileInfo) bool {
	if wipesHomeDir(cliPath) {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	if abs, err := filepath.Abs(home); err == nil {
		home = abs
	}
	if folderIsAtOrAbove(fi, home) {
		return true
	}
	final := finalDirPath(home)
	return final != home && folderIsAtOrAbove(fi, final)
}

// folderIsAtOrAbove reports whether the folder of fi is p or a parent folder of
// p. A step of p that Stat cannot read is skipped, and the walk goes on.
func folderIsAtOrAbove(fi os.FileInfo, p string) bool {
	for {
		if pi, err := os.Stat(p); err == nil && os.SameFile(fi, pi) {
			return true
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false
		}
		p = parent
	}
}

// validateCLIVersion rejects a -cli-version the install cannot honestly carry
// out. Two rules, both claustrum-only.
//
//  1. A SINGLE PATH COMPONENT (D6). cliPath is installCLIPath(cliDir, cliVersion)
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

// cliRunBound stops the direct `<cli> --version` run of -install on a cache hit.
// cliFirstRunBound stops the direct run of a CLI that this -install run put in
// place. Both are reference behavior, measured on f6010b97 and 89cb6289.
//
//   - A present CLI that answers at 28 s passes (row B03). One that answers at
//     33 s is stopped at 30.0 s (row B04). Both on Linux, macOS and Windows.
//   - A present CLI that answers at 31 s is stopped (row B15, Linux and Windows).
//   - A new CLI that answers at 118 s passes (row F04). One that answers at
//     123 s is stopped at 120.0 s (row F05). Both on Linux, macOS and Windows.
//   - A new CLI that answers at 121 s is stopped (row F15, Linux and Windows).
//   - The class follows the install in this run, not the existence of a file
//     before the run (row F14, Linux).
//
// A "direct" run is a run with no usable managed launcher. A launcher run has
// its own bounds (managedRunBound, managedFirstRunBound).
//
// The bounds are always on. They are vars only so tests shrink them.
var (
	cliRunBound      = 30 * time.Second
	cliFirstRunBound = 120 * time.Second
)

// cliUnresponsiveText is the cliError of a direct run that its bound stopped. It
// is copied from a VM capture, and it is the same text for both bounds.
const cliUnresponsiveText = "cli unresponsive: the installed Claude Code binary started but did not answer --version within 30s (120s for a first run) and was stopped; the host is not letting it run (endpoint security software or a stalled network home are the usual causes)"

// cliRunResult is the outcome of one direct `<cli> --version` run.
type cliRunResult int

const (
	cliRunOK      cliRunResult = iota // exited 0
	cliRunFailed                      // did not start, or exited non-zero
	cliRunStopped                     // still ran at the bound and was stopped
)

// startCLIRun is (*exec.Cmd).Start behind a seam, so a test can make the start
// call slow. Production never reassigns it.
var startCLIRun = (*exec.Cmd).Start

// afterCLIBound is time.AfterFunc behind a seam, so a test can hold the stop
// step of runCLIVersion. Production never reassigns it.
var afterCLIBound = time.AfterFunc

// runCLIVersion runs `<path> --version` and stops it at bound.
//
// The clock starts right before the process start call. Measured on a Windows
// VM against 89cb6289: a stopped run has a wall time of 30.01 to 30.06 s, or
// 120.02 to 120.06 s. The start delay of the CLI does not change it. A clock
// that starts after the start call stops 0.07 to 0.30 s later there. That the
// clock starts before the start call is inferred from those wall times. On Linux
// and macOS a process starts in a few milliseconds. Both readings fit the rows.
// A download before the run does not use up the bound (row F13).
//
// The CLI runs in its own process group (newSysProcAttr), as on the reference
// (Linux and macOS: the stub's pgid is its pid). At the bound the whole group is
// killed on Linux and macOS. A child in that group ends with the CLI, and a
// child in a new session survives (rows B08, B09, F10, F11). On Windows only the
// CLI process is ended and its children stay (rows B08, B09). That is the
// teardown of probeCLIRunnable.
//
// Not measured: the name of the stop signal. The stub logs no signal and a stub
// that ignores SIGTERM is stopped at the same time (rows B07, F09), so claustrum
// sends SIGKILL.
//
// The CLI gets no output pipe: stdout and stderr are the null device. A process
// that holds the CLI's output after the CLI exits therefore delays nothing
// (row B14). Not measured: what the reference gives the CLI as stdin, stdout and
// stderr, and its environment beyond the launcher gate variable, which stays.
func runCLIVersion(path string, bound time.Duration) cliRunResult {
	cmd := exec.Command(path, "--version")
	cmd.SysProcAttr = newSysProcAttr()
	began := time.Now()
	if err := startCLIRun(cmd); err != nil {
		return cliRunFailed
	}
	// The stop step and the end of cmd.Wait share one lock. After cmd.Wait the
	// pid of the CLI is free, and so is its group id, so a group kill then hits
	// whatever holds that id. A stop step that starts after cmd.Wait returned
	// therefore does nothing. A stop step that is already in its kill holds the
	// lock, and the flag is set only after it finished. time.Timer.Stop does not
	// wait for a stop step that started, so this function waits for it: no
	// goroutine of this run is left when it returns.
	var (
		mu      sync.Mutex
		waited  bool // cmd.Wait returned
		stopped bool // the stop step killed the CLI
	)
	fired := make(chan struct{})
	timer := afterCLIBound(bound-time.Since(began), func() {
		defer close(fired)
		mu.Lock()
		defer mu.Unlock()
		if waited {
			return
		}
		stopped = true
		reapProcessGroup(cmd.Process)
		_ = cmd.Process.Kill()
	})
	err := cmd.Wait()
	mu.Lock()
	waited = true
	mu.Unlock()
	if !timer.Stop() {
		<-fired
	}
	switch {
	case err == nil:
		return cliRunOK
	case stopped:
		return cliRunStopped
	}
	return cliRunFailed
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
// `<cli> --version` probe. Measured on 89cb6289 (rows Q1 and Q2, Linux, macOS and
// Windows): a CLI that answers at 28 s passes, and one that answers at 33 s gives
// __CLI_HUNG__ at 30.0 s. -probe-cli is a standalone mode Claude Desktop drives to
// classify a CLI binary, and the reference always bounds it.
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
// non-zero (bad).
//
// On Windows a path with no file at exactly that name is bad, and nothing starts
// (probeCLIPathMissing).
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
	if probeCLIPathMissing(path) {
		return probeCLIBad
	}
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
// pays cannot decline.
//
// Zero is the stdlib's own "no timeout" sentinel, so assigning it straight through
// IS the bypass — no huge-but-finite value stands in for "off", which is the same
// property D3 and D10 get by skipping their `io.LimitReader`s.
//
// ⚠️ That disables the bound on the BODY READ, not every clock on the path.
// fetchToFile uses a clone of http.DefaultTransport, which carries
// net.Dialer{Timeout: 30s} and TLSHandshakeTimeout: 10s. A host that
// black-holes SYN still fails at 30 s with the bound "off". Those are stdlib
// defaults, always-on, and unnumbered — neither has been probed on the reference.
// The wait for the response headers has its own always-on limit
// (installHeaderTimeout), which is reference behavior.
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
	// The transport is the stdlib default plus the response-header limit, so the
	// proxy, dial and TLS settings stay the defaults.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = installHeaderTimeout
	// The transport keeps the connection after the body. Do not close the idle
	// connections here. The cloned default transport has an idle limit of 90 s
	// (IdleConnTimeout), and that closes it. Measured on a macOS VM (cells F08b,
	// L08b, L09b): 89cb6289 and claustrum both close it 90.0 to 90.1 s after it.
	client := &http.Client{Timeout: cliDownloadTimeout, Transport: tr}
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
	// once in every attempted install and takes old ".fetch-*" and "*.zst" from the
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

// zstPartMinAge is how old a "*.zst.part" entry must be before sweepZstParts
// removes it. Cells Z1 of 89cb6289 straddle it on Linux, macOS and Windows VMs:
// a file that is 6 days and 23 h old stays, and a file that is 7 days and 1 h
// old goes. The exact 7 day point is not measured, and claustrum treats it as
// not old.
const zstPartMinAge = 7 * 24 * time.Hour

// isZstPartName reports whether a cli-dir entry has the "*.zst.part" name.
// sweepZstParts removes such an entry by zstPartMinAge, and the prune does not
// count it. The match is case-sensitive: "X.ZST.PART" stays at 9 days (cells
// Z3, Linux, macOS and Windows VMs, 89cb6289).
//
// The name wins over the ".fetch-" prefix. Cells Z2 on the same three systems:
// the file ".fetch-a.zst.part" is 20 minutes old and stays. Not measured: such a
// name that is more than 7 days old. claustrum removes it as every other
// "*.zst.part" name.
func isZstPartName(name string) bool { return strings.HasSuffix(name, ".zst.part") }

// sweepZstParts removes each "*.zst.part" entry of the cli-dir that is more
// than zstPartMinAge old, with one os.Remove. runInstall calls it once, before
// the run of the CLI that is present. Cell Z4b of 89cb6289 (Linux VM): on a
// cache hit an 8 day old "p.zst.part" file is gone when that CLI runs. Cell Z4a
// (Linux VM): it is gone too when the new CLI exits 1. Rows C-10 (Linux and
// macOS VMs) and cell C-18 (Windows VM): it is gone when a new CLI runs.
//
// Not measured: a cache hit on macOS and Windows, and a run with no CLI of the
// version and no source. claustrum sweeps there as in cell Z4b.
//
// An entry that is the home folder or holds it stays at every age. See
// cliEntryHoldsHome.
func sweepZstParts(cliDir string, now time.Time) {
	sweepOld(cliDir, now, zstPartMinAge, isZstPartName)
}

// sweepFetchTemps removes install litter from the cli-dir: an entry whose name
// isSweptName claims AND whose mtime is more than sweepMinAge before now. The
// litter is an interrupted install's ".fetch-<something>" or a stray "*.zst".
// A "*.zst.part" name is not litter of this pass, also with the ".fetch-"
// prefix (see isZstPartName).
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
//
// An entry that is the home folder or holds it stays at every age. See
// cliEntryHoldsHome.
func sweepFetchTemps(cliDir string, now time.Time) {
	sweepOld(cliDir, now, sweepMinAge, func(name string) bool {
		return isSweptName(name) && !isZstPartName(name)
	})
}

// sweepOld is the loop of both sweeps. It removes each entry of the cli-dir
// that claims names and whose own mtime is more than minAge before now, with
// one os.Remove.
func sweepOld(cliDir string, now time.Time, minAge time.Duration, claims func(name string) bool) {
	ents, err := os.ReadDir(cliDir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !claims(e.Name()) {
			continue
		}
		p := filepath.Join(cliDir, e.Name())
		fi, err := os.Lstat(p)
		if err != nil || now.Sub(fi.ModTime()) <= minAge {
			continue
		}
		// The home guard (D2).
		if cliEntryHoldsHome(p, fi) {
			continue
		}
		_ = os.Remove(p)
	}
}

// cliEntryHoldsHome is the home guard of the sweeps and of the prune (D2). p is
// an entry of the cli-dir and fi is its Lstat answer. A folder gets both tests
// of cliFolderHoldsHome. Every other kind gets wipesHomeDir alone.
//
// Each of those removes is one os.Remove, so only an EMPTY home folder can go
// there. A cli-dir that is the parent of the home folder makes the home folder
// an entry. 89cb6289 removes an empty home folder there: in the prune (cells
// H1, Linux, macOS and Windows VMs) and as "h.zst" in the sweep (cells H3,
// Linux and macOS VMs). claustrum keeps it. That difference stays by the
// maintainer's decision.
func cliEntryHoldsHome(p string, fi os.FileInfo) bool {
	if fi.IsDir() {
		return cliFolderHoldsHome(p, fi)
	}
	return wipesHomeDir(p)
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

// pruneCLI is the -cli-keep prune. It runs after a good install only. The rows
// are the C rows of 89cb6289: C-0 to C-15 on Linux and macOS VMs, and the cells
// C-00 to C-18 on a Windows VM. A cell id has two digits.
//
//   - Every entry of the cli-dir counts, whatever its kind: a file, a folder
//     and a link. Rows C-1 (four empty folders, keep 3, the two oldest go) and
//     C-5 (two files and two newer empty folders, keep 3, both files go). On
//     Windows the cells C-01 and C-04.
//   - The order is the mtime, newest first. The keep value is how many stay.
//     Rows C-3 (keep 5 for five entries, all stay) and C-4 (keep 4, the oldest
//     goes) straddle it.
//   - Entries with the same mtime stay in name order, so the later name goes.
//     Rows C-12 and C-12b on Linux (the order of creation does not matter),
//     C-12 and C-12b on macOS, cell C-15 on Windows.
//   - The new CLI has no place of its own. It stands where its mtime puts it.
//     Rows C-11 (two folders with an mtime 1 h in the future, keep 2: the new
//     CLI goes, and the result line names its path with no cliError) against
//     C-6 (no future mtime, the new CLI stays). On Windows the cells C-11dir
//     and C-11file.
//   - Each remove is one os.Remove of a direct child of the cli-dir. It removes
//     a file, an EMPTY folder and a link. A folder with content stays, and a
//     link goes as a link. Rows C-1 against C-2 (the same folders, one file in
//     each, all stay), C-6 and C-7. On Windows a folder with the read-only
//     attribute stays (cell C-05) and a read-only file goes (cell C-07).
//   - A name that the sweep claims is not counted and not removed here, at any
//     age. Rows C-9 (a fresh ".fetch-d" and "x.zst" stay and three other
//     entries stay with keep 3) and C-8 (20 minutes old, the sweep took both
//     before the new CLI ran).
//   - A "*.zst.part" name is not counted and not removed here either. Cell
//     C-18 on a Windows VM: the file "q.zst.part" is 6 days old and older than
//     every other entry, keep is 3 and five other entries count, and it stays.
//     sweepZstParts removes such a name by its own age rule (zstPartMinAge).
//   - Keep 0 removes every entry that counts, the new CLI too, and a folder
//     with content stays. The result line still names the CLI path, with exit
//     code 0 (cells K0a and K0b, Linux, macOS and Windows VMs, and cell C-13).
//   - The name order of a tie is the order of the bytes, so "B" comes before
//     "a". Cells T1 (files) and T1d (folders): "B", "a" and "c" have one mtime,
//     keep is 2, and "B" stays beside the new CLI. Files ran on Linux, macOS
//     and Windows VMs, folders on macOS and Windows VMs.
//   - A link to a folder with content outside the cli-dir goes, and the target
//     stays (cells S1, Linux and macOS VMs).
//
// runInstall does not call the prune for a negative keep value. See there.
//
// The mtime comes from os.Lstat, as in the sweep. That choice is from the
// code, not from a row.
//
// Not measured:
//   - Whether a folder with content takes a place in the order. claustrum
//     counts it as every other entry. Cells F1 (a folder with content that is
//     older than every other entry, keep 3: the two oldest files go and the
//     folder stays) give the same result both ways.
//   - A swept name that the sweep failed to remove, for example an old
//     ".fetch-d" folder with content. claustrum does not count it, as before.
//
// One difference from 89cb6289 (D18): a ".blob-" name is not counted and not
// removed here. Cell C-12 on a Windows VM: a planted ".blob-planted" file is
// 1 h old beside two files that are 4 h and 3 h old, keep is 2, and 89cb6289
// removes both older files. claustrum removes the oldest only. Cells B1
// (Linux, macOS and Windows VMs): the planted file is the oldest entry, keep is
// 1, and 89cb6289 removes it. claustrum keeps it. The name is
// claustrum's own temporary name of a download in progress, and a download of
// a second install must not use a keep place. That is the maintainer's decision
// of 2026-10-07.
//
// An entry that is the home folder or holds it is counted and never removed.
// See cliEntryHoldsHome.
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
		if isSweptName(e.Name()) || isZstPartName(e.Name()) {
			continue
		}
		// A concurrent install's in-flight download blob is not a CLI version.
		// Counted, it sorts NEWEST, takes a -cli-keep slot and evicts a real
		// binary. 89cb6289 counts a planted file with this name (cell C-12,
		// Windows VM). This is D18.
		if isDownloadBlobName(e.Name()) {
			continue
		}
		if fi, err := os.Lstat(filepath.Join(cliDir, e.Name())); err == nil {
			vs = append(vs, ver{e.Name(), fi.ModTime().UnixNano()})
		}
	}
	// os.ReadDir gives the entries in name order, and the sort is stable.
	sort.SliceStable(vs, func(i, j int) bool { return vs[i].mod > vs[j].mod })
	for i := keep; i < len(vs); i++ {
		p := filepath.Join(cliDir, vs[i].name)
		// The home guard (D2). The entry is skipped here and not in the loop
		// above, so it still takes its place in the order and the other entries
		// go as without it. That is claustrum's own choice and is not measured.
		if fi, err := os.Lstat(p); err == nil && cliEntryHoldsHome(p, fi) {
			continue
		}
		_ = os.Remove(p)
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
	ctx, cancel := context.Background(), context.CancelFunc(func() {})
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}
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
