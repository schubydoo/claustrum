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
	// Reset the -cli-url download sink; fetchCLI fills it when a download is
	// attempted, and it is read back into f.Fetch below (4534d86).
	lastInstallFetch = nil
	lastInstallFinal = nil
	installSwept = false
	installBlob = o.cliZst
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

		// An old "<version>.zst.part" file goes before the CLI runs, on every
		// path. On a good cache hit only this sweep runs (cell L9 of 5fd08069).
		sweepZstParts(o.cliDir, time.Now())

		// "present" requires the file to exist AND be runnable (real binary checks
		// `<cli> --version`). A freshly downloaded CLI leaves cliWasPresent false.
		// On Windows only <version>.exe counts. A file with the bare name counts
		// for nothing (rows W02 to W05).
		//
		// With the launcher gate on, a file that is no program of this machine
		// does not run, and the install runs over it (cliFileLooksRunnable).
		// The cli folder is made before the CLI is looked for, also on a cache
		// hit (makeCLIDir). A failure takes the arm of a failed install below
		// (claustrum's own, not measured). So a present CLI with D/sub a regular
		// file gets no probe, the mkdir error and an empty cliPath. The 10 minute
		// sweep then finds no folder to sweep.
		dirErr := makeCLIDir(o, f.CliPath)
		checkErr := errCLINotRunnable
		if dirErr == nil && isRegularFile(f.CliPath) && (installManaged == nil || cliFileLooksRunnable(f.CliPath)) {
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
			// and it sweeps. The "<version>.zst.part" sweep ran above. On
			// 5fd08069 a good cache hit removes only an old 1.0.0.zst.part, and
			// .fetch-123 and 1.0.0.zst stay (cell L9, Linux VM).
			f.CliWasPresent = true
		} else {
			err := dirErr
			if err == nil {
				err = ensureCLI(o, f.CliPath)
			}
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
			// installSwept. Every other path sweeps here. The "<version>.zst.part"
			// sweep is not in this table: it ran above, on every path.
			if !installSwept {
				sweepFetchTemps(o.cliDir, time.Now())
			}
			if err == nil {
				// A negative keep counts as 0. On 5fd08069 the result line
				// follows with exit 0 and an empty stderr. The older version
				// files go (cells L6, M2b and W4b, Linux, macOS and Windows).
				pruneCLI(o.cliDir, o.cliKeep, filepath.Base(f.CliPath))
			}
		}
	}

	// The reference appends the fetch object whenever a -cli-url download ran (set by
	// fetchCLI), including a cache hit that still downloaded is impossible — a
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

// installBlob is the -cli-zst blob of this -install run. runInstall sets it.
// The sweeps skip the entry that is this file, so a failed install keeps its
// blob. On 5fd08069 a blob C/1.0.0.zst that is 20 minutes old and no zstd
// archive stays (cell L3, Linux VM).
var installBlob string

// cliDirError is a cli folder that cannot be made. Its text lands in cliError,
// and runInstall answers an empty cliPath for it.
type cliDirError struct{ err error }

func (e *cliDirError) Error() string { return fmt.Sprintf("mkdir cli dir: %v", e.err) }

// makeCLIDir is the folder step of runInstall. It runs before the CLI is looked
// for, so a cache hit makes the folders too (cell L-V3 of round tier3-01).
//
// The mkdir uses the -cli-dir text as given, not the cleaned folder of cliPath.
// So D/sub/.. makes D/sub, and D/a/b/../.. makes D/a and D/a/b. With D/sub a
// regular file the answer is "mkdir cli dir: mkdir D/sub: not a directory", and
// the file stays. 5fd08069 does the same in cells L-HS10b, L-V1, L-V2 and L-V3
// (Linux VM) and M-HS10b (macOS VM). On Windows the step is off
// (rawCLIDirStep). There 5fd08069 makes nothing and answers the "missing" error,
// also with sub a file (cells W-HS10b and W-V1, Windows VM).
//
// clearNonFolderCLIDir keeps the cleaned folder of cliPath, as before. With the
// raw text a ".." after a symbolic link names another entry. The cleaned folder
// keeps its removes on the entries of before (claustrum's own, not measured).
// Its place before the mkdir is not measured.
//
// A version that D6 refuses skips this step, so the refusal of ensureCLI still
// comes before any write or remove at a path built from the version
// (claustrum's own).
func makeCLIDir(o installOpts, cliPath string) error {
	if !rawCLIDirStep || validateCLIVersion(o.cliVersion) != nil {
		return nil
	}
	clearNonFolderCLIDir(filepath.Dir(cliPath))
	if err := os.MkdirAll(o.cliDir, 0o700); err != nil {
		return &cliDirError{err: err}
	}
	return nil
}

// hashBlobFile is sha256File behind a seam, so ensureCLI's -cli-zst hash-failure arm is
// reachable from a test: the blob has just been opened and read a byte at that
// point, so no fixture can make the very next hash of the same path fail.
// Production never reassigns it.
var hashBlobFile = sha256File

// ensureCLI materializes the CLI binary from a .zst blob: either an already
// uploaded one (-cli-zst, SFTP fallback) or a download (-cli-url). The blob is
// zstd-decompressed into a staging file and then verified against -cli-checksum
// (always on -cli-url, on -cli-zst only when one is supplied). The staging file
// is then renamed to cliPath.
func ensureCLI(o installOpts, cliPath string) error {
	// Validate -cli-version before the install writes or removes anything at a
	// path built from the version. The rules and their measurements live on
	// validateCLIVersion. Both are claustrum-only hardening.
	//
	// The two sweeps of the cli-dir are not behind this test. They name only the
	// cli-dir, never the version. runInstall runs the "<version>.zst.part" sweep
	// before this call, and the 10 minute sweep after this call answered the
	// refusal.
	//
	// Guarding here rather than at the individual hazards gates every write and
	// remove at such a path, not just the destructive one, and reports through
	// the existing cliError field so the facts frame keeps its shape. Skipped
	// when cliDir is unset — then cliPath is not derived from a version and
	// there is nothing to contain.
	if o.cliDir != "" {
		if err := validateCLIVersion(o.cliVersion); err != nil {
			return err
		}
	}
	// The source is decompressed straight into the staging file. Nothing reads
	// the blob or the body into memory, and the checksum comes after the
	// decompress, before the chmod, the clear and the rename (stageAndInstall).
	// On 5fd08069 a -cli-url body that is no zstd archive answers the decoder
	// text with a right and with a wrong checksum. fetch.bytes is the count
	// that the decoder read (cells L15a and L15b, Linux VM). The -cli-zst blob
	// with a checksum follows the same order. 7d193f89 through f6010b97 answer
	// the decoder text there (Linux VM). It is not measured on 5fd08069.
	//
	// Measured with a 400 MiB incompressible blob (peak RSS from /proc VmHWM),
	// on the earlier path that wrote the download to a file first:
	//
	//	-cli-zst   410 MB -> 9 MB
	//	-cli-url   886 MB -> 10 MB   (ReadAll peaked at ~2x the body while growing)
	//
	// The streamed decode of -cli-url is not measured.
	//
	// On every cache miss the cli-dir is created FIRST, before the source check,
	// before the blob is opened and before any network access. The reference
	// creates the cli-dir (0700, with its parents) there. A Linux VM measured it
	// on -cli-zst from 5db5e4a to f6010b97. It measured -cli-url and a miss with
	// no source flag on f6010b97. A failure leaves the cli-dir, empty if it was
	// new. A missing blob, a 404, a refused connection and a missing source all
	// leave it. A -cli-url download also shows it 1.5 s into a 3 s wait for
	// headers. On Linux and macOS, makeCLIDir made the folders of the raw
	// -cli-dir text before the cache probe. This step then makes the cleaned one.
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
	var stage func(dir string) (string, error)
	switch {
	case o.cliZst != "":
		// SFTP fallback. If the caller supplies a -cli-checksum, the reference checks the blob
		// against it since 7d193f89. 5db5e4a ignored it. A mismatch answers
		// "checksum mismatch" and keeps the blob. An absent or empty checksum
		// verifies nothing.
		//
		// A blob that cannot be opened answers "opening input: ". A folder opens,
		// so its read error comes from the decoder: "decompressing: read <p>: is
		// a directory" on 5fd08069 (cell L13, Linux VM).
		f, oerr := os.Open(o.cliZst)
		if oerr != nil {
			return fmt.Errorf("opening input: %v", oerr)
		}
		_ = f.Close()
		stage = func(dir string) (string, error) { return stageBlob(o.cliZst, o.cliChecksum, dir) }
	case o.cliURL != "":
		stage = func(dir string) (string, error) { return stageDownload(o.cliURL, o.cliChecksum, dir) }
	default:
		return fmt.Errorf("cli %s missing and no --cli-url or --cli-zst provided", o.cliVersion)
	}
	// Stage, verify and install, retried ONCE if a concurrent install's sweep
	// reclaimed our staging file. See stageAndInstall for when that can happen.
	// A download is not fetched again. Its staging file is new at the rename,
	// so the age gate of the sweep keeps it (claustrum's own, not measured: a
	// concurrent install can take a .fetch- file during a download with no
	// output for 10 minutes).
	err := stageAndInstall(cliPath, stage, o.cliZst)
	if err != nil && errors.Is(err, errStagingVanished) && o.cliZst != "" {
		err = stageAndInstall(cliPath, stage, o.cliZst)
	}
	return err
}

// stagingError is a staging file that cannot be made in the cli folder.
type stagingError struct{ err error }

func (e *stagingError) Error() string { return fmt.Sprintf("staging cli: %v", e.err) }

// newStagingFile makes the empty staging file ".fetch-<random>" in dir.
func newStagingFile(dir string) (string, error) {
	f, err := os.CreateTemp(dir, ".fetch-*")
	if err != nil {
		return "", &stagingError{err: err}
	}
	_ = f.Close()
	return f.Name(), nil
}

// stageBlob decompresses the -cli-zst blob into a new staging file in dir and
// returns its path. With a checksum, the blob is verified after the
// decompress. A failure removes the staging file.
func stageBlob(blob, checksum, dir string) (string, error) {
	tmp, err := newStagingFile(dir)
	if err != nil {
		return "", err
	}
	if err := zstdDecompress(blob, tmp); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("decompressing: %v", err)
	}
	if checksum != "" {
		sum, err := hashBlobFile(blob)
		if err != nil {
			err = fmt.Errorf("opening input: %v", err)
		} else {
			err = verifyChecksum(sum, checksum)
		}
		if err != nil {
			_ = os.Remove(tmp)
			return "", err
		}
	}
	return tmp, nil
}

// stageDownload downloads the -cli-url body into a new staging file in dir,
// decompressed as it arrives, and returns its path. The body is verified
// UNCONDITIONALLY after that. An empty -cli-checksum still fails
// ("checksum mismatch: expected=, actual=<sha>"), as on the reference.
func stageDownload(url, checksum, dir string) (string, error) {
	tmp, sum, err := fetchCLI(url, dir)
	if err != nil {
		return "", downloadError(err)
	}
	if err := verifyChecksum(sum, checksum); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	// The final progress line comes only after the checksum passes. Measured
	// on a Linux VM against f6010b97, 5 of 5 runs per row: a good download
	// prints bytes:0 then bytes:57, and a mismatch prints bytes:0 only.
	emitFinalProgress()
	return tmp, nil
}

// downloadError gives an error of fetchCLI its cliError form. Five kinds are
// fully worded and go out bare. They are a status failure, a read-idle stall, a
// broken body read, a refused archive and a staging failure. Every other
// download error (a transport failure, the D10 cap, the D12 deadline) takes the
// "download failed: " prefix. The reference's cliError is "download failed with
// status N" for a non-200 and the bare "download stalled: …" for a stall (see
// httpStatusError, stalledError). A body read that ended with a transport error
// is row E07 (see interruptedError).
func downloadError(err error) error {
	var (
		se *httpStatusError
		st *stalledError
		it *interruptedError
		de *decompressError
		sg *stagingError
	)
	if errors.As(err, &se) || errors.As(err, &st) || errors.As(err, &it) || errors.As(err, &de) || errors.As(err, &sg) {
		return err
	}
	return fmt.Errorf("download failed: %v", err)
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

// stageAndInstall makes the staging file with stage, puts it at cliPath and
// then runs it. It follows the order of the reference, measured on 89cb6289:
//
//  1. What is at cliPath goes before the new CLI runs. A folder there is removed
//     as a tree, with the file in it (row E11 on Linux and macOS, row E11b on
//     Windows). A file there is replaced (row F14, Linux).
//  2. The new CLI is at cliPath. The sweep has run, and the -cli-zst blob is
//     gone. The cli folder holds no `.fetch-` temp of this install (rows F01, F02
//     and W01, on Linux, macOS and Windows). On 5fd08069 the blob is gone when
//     the new CLI runs (cells L11, M4 and W7, Linux, macOS and Windows VMs).
//  3. `<cliPath> --version` runs. The CLI and a managed launcher get the final
//     path (rows L06, L08 to L10).
//  4. A stopped run keeps the new CLI (rows F05 to F11, F14, F15, L07, L09). A
//     run that exits non-zero removes it and answers the "not runnable" text with
//     the final path (rows F12 and G05).
//
// Every failure before step 2 keeps the -cli-zst blob. On 5fd08069 it stays
// after an archive that is no zstd (cell L3) and a folder that cannot be
// cleared (cells L12 and M5). It stays after a failed rename (cells W1 and W6).
// claustrum keeps it after a failed chmod too (claustrum's own, not measured).
//
// The delete of step 1 is an os.RemoveAll of cliPath. The delete of step 4 is
// an os.Remove of the file that this call put there. ensureCLI runs
// validateCLIVersion (D6) before it calls this function, so cliPath is a direct
// child of the cli folder. D6 does not say which folder that child is, so
// folderHoldsHome runs right before the delete of step 1. That guard is a
// divergence (D2): the reference removes a home folder there.
//
// Not measured: how the reference writes the final file, and the order of its
// sweep against that write. claustrum writes a staging file and renames it, so
// cliPath only ever holds a complete 0755 binary, and it sweeps after the rename.
// Not measured either: a new CLI that exits non-zero over a file or a folder at
// cliPath. claustrum then removes the new CLI as for an empty one.
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
// retries a -cli-zst install once on errStagingVanished.
//
// chmodStaged is os.Chmod behind a seam, so the one branch between decompress
// and rename that no fixture can otherwise provoke is reachable from a test.
// Production never reassigns it.
var chmodStaged = os.Chmod

func stageAndInstall(cliPath string, stage func(dir string) (string, error), blob string) error {
	tmp, err := stage(filepath.Dir(cliPath))
	if err != nil {
		return err
	}
	if err := chmodStaged(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if fi, err := os.Lstat(cliPath); err == nil && fi.IsDir() {
		// The home guard (D2). A folder at cliPath that is the home folder, or
		// that contains it, is not removed. The reference removes it as a tree
		// (measured on Linux, macOS and Windows, see docs/DIVERGENCES.md D2). See
		// folderHoldsHome. The folder is not removed here, and the -cli-zst
		// blob stays.
		if folderHoldsHome(cliPath, fi) {
			_ = os.Remove(tmp)
			return fmt.Errorf("cli path must not be or contain the home directory: %q", cliPath)
		}
		if rmErr := os.RemoveAll(cliPath); rmErr != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("clearing stale dir at %s: %v", cliPath, rmErr)
		}
	}
	if err := placeStagedCLI(tmp, cliPath); err != nil {
		return err
	}
	if blob != "" {
		_ = os.Remove(blob)
	}
	sweepFetchTemps(filepath.Dir(cliPath), time.Now())
	installSwept = true
	err = installCLICheck(cliPath, true)
	var stopped *cliStoppedError
	if err == nil || errors.As(err, &stopped) {
		return err
	}
	_ = os.Remove(cliPath)
	return fmt.Errorf("installed cli at %s is not runnable", cliPath)
}

// cliRenameBound is the time from the first try in which a denied rename of
// the new CLI is tried again (Windows). On 5fd08069 a running old CLI with
// other bytes keeps the staging file 0.26 to 0.53 s (cells W1, Windows VM).
// With this bound the staging file of claustrum lived 0.35 to 0.48 s in cell
// W1 of round task090-02, inside that range. The pauses of retryWhile are
// claustrum's own. A test replaces the bound.
var cliRenameBound = 200 * time.Millisecond

// placeStagedCLI renames the staging file tmp to cliPath. Each error has the
// prefix "rename: ". On 5fd08069 the text is "rename: rename <tmp> <cli>:
// Access is denied." (cells W1 and W6, Windows VM). The prefix on Linux and
// macOS is claustrum's own.
//
// On Windows a rename that access denied stops is tried again for
// cliRenameBound (renameDenied). If it still fails and cliPath holds the bytes
// of tmp, the install counts as done and tmp goes with a plain remove. On
// 5fd08069 a running old CLI with the same bytes gives no cliError, and the
// blob goes (cells W2, Windows VM).
func placeStagedCLI(tmp, cliPath string) error {
	err := retryWhile(cliRenameBound, renameDenied, func() error { return os.Rename(tmp, cliPath) })
	if err == nil {
		return nil
	}
	if _, statErr := os.Lstat(tmp); statErr != nil {
		// Our staging file is gone. A concurrent install's sweep took it.
		// A file at cliPath is deliberately left alone.
		return fmt.Errorf("rename: %w before install: %v", errStagingVanished, err)
	}
	if renameDenied(err) && sameFileBytes(tmp, cliPath) {
		_ = os.Remove(tmp)
		return nil
	}
	_ = os.Remove(tmp)
	return fmt.Errorf("rename: %v", err)
}

// sameFileBytes reports whether the files a and b have one SHA-256. A file
// that cannot be read is not the same.
func sameFileBytes(a, b string) bool {
	sa, err := sha256File(a)
	if err != nil {
		return false
	}
	sb, err := sha256File(b)
	return err == nil && sa == sb
}

// folderHoldsHome is the home guard of the tree delete in stageAndInstall.
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
func folderHoldsHome(cliPath string, fi os.FileInfo) bool {
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
// out. The rule is claustrum-only: a SINGLE PATH COMPONENT (D6). cliPath is
// installCLIPath(cliDir, cliVersion) and ensureCLI's os.RemoveAll deletes
// cliPath recursively, so a version that reaches outside cliDir destroys
// unrelated data. Measured, the reference destroys the target on "../victim".
// "../victim" escapes because Join CLEANS. "link/1.0.0" escapes through a
// symlink under cliDir, which a lexical containment check accepts because it is
// lexically inside.
//
// A version that the sweep claims (".fetch-1", "1.0.0.zst") is ACCEPTED, as the
// reference accepts it. Since 4534d86 the sweep removes only entries more than
// ten minutes old, so such a version installs and stays until a later install
// finds it that old. Measured on a Linux VM against 4534d86 through f6010b97.
// claustrum refused these versions until then (the retired D7).
//
// "." and ".." are rejected explicitly: "." resolves cliPath to the cli-dir
// ITSELF, which hands the whole cli-dir to os.RemoveAll.
//
// BOTH separators are rejected on every OS, not just the local one. A backslash
// is a legal filename byte on Unix, so this is stricter than the platform
// requires — deliberately, so a Unix daemon cannot be handed a path that a
// Windows client built, and so the accepted set does not change with GOOS.
//
// No real version string trips the rule: 1.0.86, 2.0.0-beta.1, a commit sha,
// "latest" and 1.0.86+build.5 are all measured as accepted.
func validateCLIVersion(v string) error {
	if !isSingleComponent(v) {
		return fmt.Errorf("cli version %q must be a single path component", v)
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
// zstdDecompress, and the downloaded body in fetchCLI. A crafted .zst can be
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
	return decodeZstdTo(in, dest)
}

// decodeZstdTo decompresses the zstd stream r into the file dest, which it
// creates or truncates. A failed close of dest is an error too.
func decodeZstdTo(r io.Reader, dest string) (err error) {
	dec, err := zstd.NewReader(r)
	if err != nil {
		return err
	}
	defer dec.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := closeFetchTemp(out); err == nil {
			err = cerr
		}
	}()
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

// decompressError is a -cli-url body that the zstd decoder refused. Its text
// is the cliError, with the "decompressing: " prefix.
type decompressError struct{ err error }

func (e *decompressError) Error() string { return fmt.Sprintf("decompressing: %v", e.err) }

func (e *decompressError) Unwrap() error { return e.err }

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

// cliDownloadTimeout bounds the whole `-cli-url` exchange in fetchCLI.
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
// fetchCLI uses a clone of http.DefaultTransport, which carries
// net.Dialer{Timeout: 30s} and TLSHandshakeTimeout: 10s. A host that
// black-holes SYN still fails at 30 s with the bound "off". Those are stdlib
// defaults, always-on, and unnumbered — neither has been probed on the reference.
// The wait for the response headers has its own always-on limit
// (installHeaderTimeout), which is reference behavior.
// Opt in with
// -cli-download-timeout or the cli-download-timeout key in claustrum.conf. Also
// set directly by tests. Divergence D12.
var cliDownloadTimeout time.Duration

// closeFetchTemp is (*os.File).Close behind a seam over the close of the
// staging file in decodeZstdTo, the one close whose error counts. That close
// fails only on a deferred write-back error, for example a full filesystem. No
// fixture can provoke that portably. Without the seam the arm that removes the
// half-written staging file is unreachable. Production never reassigns it.
var closeFetchTemp = (*os.File).Close

// fetchCLI downloads url and decompresses the body as it arrives into a new
// staging file in dir, the cli-dir. It returns that file's path and the sha256
// of the whole body, computed WHILE streaming. So the body is never held in
// memory and never hashed in a second pass. The caller owns the staging file.
//
// A decoder that refuses the body stops the read. fetch.bytes is then the count
// that the decoder read. On 5fd08069 a body of 4096 bytes that is no zstd archive
// gives "fetch":{"bytes":4,…} and one progress line (cells L15a and L15b, Linux
// VM). After a good archive the rest of the body is read for the checksum.
//
// The staging file is made after the response headers. So a cli-dir where no
// file can be made answers "staging cli: " with a fetch object. That is
// claustrum's own (not measured).
func fetchCLI(url, dir string) (path, sum string, err error) {
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
	tmp, err := newStagingFile(dir)
	if err != nil {
		lastInstallFetch = &fetchStats{Ms: time.Since(start).Milliseconds()}
		return "", "", err
	}
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
	// Same bypass as decodeZstdTo: with the cap off the body streams straight
	// through, hashed on the way past.
	var body io.Reader = wb
	if maxCLIBytes > 0 {
		body = io.LimitReader(wb, maxCLIBytes+1)
	}
	body = io.TeeReader(body, h)
	decErr := decodeZstdTo(body, tmp)
	if decErr == nil {
		_, _ = io.Copy(io.Discard, body)
	}
	// Record fetch stats even on a failure: the reference emits the fetch object on
	// a stall, not only on success.
	s := wb.stats()
	lastInstallFetch = &s
	_ = wb.Close() // stop the progress ticker
	// A failed body read wins over the decoder's answer, which only saw the
	// stream end.
	var fail error
	switch {
	case wb.failure() != nil:
		fail = wb.failure()
	case maxCLIBytes > 0 && s.Bytes > maxCLIBytes:
		fail = fmt.Errorf("response exceeds %d bytes", maxCLIBytes)
	case decErr != nil:
		fail = &decompressError{err: decErr}
	}
	if fail != nil {
		_ = os.Remove(tmp)
		return "", "", fail
	}
	lastInstallFinal = &progressLine{Phase: "download", Bytes: s.Bytes, Total: total}
	return tmp, hex.EncodeToString(h.Sum(nil)), nil
}

// sweepMinAge is how old a swept name must be before the sweep removes it.
// The gate exists from 4534d86 on. 5db5e4a and 7d193f89 swept at every age.
// Measured on a Linux VM against 19f30c46, 90fca6e6 and f6010b97: an entry
// whose mtime is 599 s old stays, and one 601 s old goes (two runs each). The
// exact 600 s point is not measured, and claustrum treats it as not old.
const sweepMinAge = 10 * time.Minute

// zstPartMinAge is how old a "<version>.zst.part" file must be before
// sweepZstParts removes it. Cells Z1 of 89cb6289 straddle it on Linux, macOS
// and Windows VMs: a file that is 6 days and 23 h old stays, and a file that
// is 7 days and 1 h old goes. Cells L10 and M3b of 5fd08069 (Linux and macOS
// VMs): a file that is 7 days and 5 s old goes. One that is 7 days less 5 s
// old stays. A file that is exactly 7 days old at the instant of the read
// is not measured, and claustrum treats it as not old.
const zstPartMinAge = 7 * 24 * time.Hour

// The names of the sweeps and of the prune, measured on 5fd08069 (cells L4, L8,
// M1, M3a, W3 and W5, Linux, macOS and Windows VMs). claustrum counts a name of
// three runs of digits with dots between them, such as 1.0.0. Other names are
// not measured.
//
//	sweep of 10 minutes   .fetch-<digits>, <version>.zst      .fetch-123, 1.0.0.zst go
//	                                                          .fetch-d, x.zst stay
//	sweep of 7 days       <version>.zst.part                  1.0.0.zst.part goes
//	                                                          p.zst.part stays
//
// Every match is case-sensitive.
func isVersionName(name string) bool {
	parts := strings.Split(name, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if !isDigits(p) {
			return false
		}
	}
	return true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// isVersionArchiveName reports whether name is "<version><suffix>".
func isVersionArchiveName(name, suffix string) bool {
	base, ok := strings.CutSuffix(name, suffix)
	return ok && isVersionName(base)
}

// isZstPartName reports whether a cli-dir entry has a name of the sweep of
// 7 days.
func isZstPartName(name string) bool { return isVersionArchiveName(name, ".zst.part") }

// isSweptName reports whether a cli-dir entry has a name of the sweep of
// 10 minutes. The staging files of claustrum are ".fetch-<digits>"
// (os.CreateTemp), so the sweep claims them.
func isSweptName(name string) bool {
	if rest, ok := strings.CutPrefix(name, ".fetch-"); ok {
		return isDigits(rest)
	}
	return isVersionArchiveName(name, ".zst")
}

// sweepZstParts removes each regular file of the cli-dir with a name of
// isZstPartName that is more than zstPartMinAge old, with one os.Remove.
// runInstall calls it once, before the run of the CLI that is present. On a
// good cache hit only this sweep runs (cell L9 of 5fd08069, Linux VM).
//
// An entry that is the home folder or holds it stays at every age. See
// cliEntryHoldsHome.
func sweepZstParts(cliDir string, now time.Time) {
	sweepOld(cliDir, now, zstPartMinAge, isZstPartName)
}

// sweepFetchTemps removes install litter from the cli-dir: a regular file whose
// name isSweptName claims AND whose mtime is more than sweepMinAge before now.
//
// The rules, measured on the reference from 4534d86 on (the future-mtime row on
// 19f30c46 through f6010b97):
//   - The age is the entry's own mtime. atime and ctime are not read.
//     os.Lstat, not DirEntry.Info: on Windows Info comes from the directory
//     listing, which can hold a stale time for a directory.
//   - A future mtime is not old, so it stays.
//
// Only a regular file goes. An empty folder ".fetch-9" stays, and so does a
// folder ".fetch-e" with a file in it (cells L8, M3a and W5 of 5fd08069).
//
// A concurrent install's staging file is fresh, so the age gate keeps it.
// stageAndInstall's retry still covers a staging file that outlives the gate.
//
// An entry that is the home folder or holds it stays at every age. See
// cliEntryHoldsHome.
func sweepFetchTemps(cliDir string, now time.Time) {
	sweepOld(cliDir, now, sweepMinAge, isSweptName)
}

// sweepOld is the loop of both sweeps. It removes each regular file of the
// cli-dir that claims names and whose own mtime is more than minAge before now,
// with one os.Remove. The -cli-zst blob of the run stays (installBlob).
func sweepOld(cliDir string, now time.Time, minAge time.Duration, claims func(name string) bool) {
	ents, err := os.ReadDir(cliDir)
	if err != nil {
		return
	}
	var blob os.FileInfo
	if installBlob != "" {
		blob, _ = os.Stat(installBlob)
	}
	for _, e := range ents {
		if !claims(e.Name()) {
			continue
		}
		p := filepath.Join(cliDir, e.Name())
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() || now.Sub(fi.ModTime()) <= minAge {
			continue
		}
		if blob != nil && os.SameFile(fi, blob) {
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
// of folderHoldsHome. Every other kind gets wipesHomeDir alone.
//
// The prune and the sweeps remove regular files only, so the guard is defense
// in depth (D2).
func cliEntryHoldsHome(p string, fi os.FileInfo) bool {
	if fi.IsDir() {
		return folderHoldsHome(p, fi)
	}
	return wipesHomeDir(p)
}

// pruneCLI is the -cli-keep prune. It runs after a good install only. newCLI is
// the file name of the CLI that this run put in place. The rules are those of
// 5fd08069 (cells L4 to L6, M1, M2a, M2b, W3, W4a and W4b, Linux, macOS and
// Windows VMs):
//
//   - Only a version file counts, and only a version file goes. On Linux and
//     macOS that is a regular file (by Lstat) whose name is a version (see
//     isVersionName) and that has an exec bit. On Windows it is a regular file
//     "<version>.exe" (isCLIVersionFile). A folder, a link, README, "1.1.0"
//     with mode 0644, "1.1.0" on Windows and README.exe stay, and none of them
//     takes a keep place.
//   - The order is the mtime, newest first. The keep value is how many stay.
//     The new CLI takes its place in the order, and it always stays. With
//     -cli-keep 0 the older version files go.
//   - A negative keep counts as 0.
//   - Entries with the same mtime stay in name order, so the later name goes.
//     The name order is the order of the bytes. That order is measured on
//     89cb6289 (cells T1, Linux, macOS and Windows VMs). On 5fd08069 it is
//     claustrum's own.
//   - Each remove is one os.Remove of a direct child of the cli-dir.
//
// The mtime comes from os.Lstat, as in the sweep.
//
// An entry that is the home folder or holds it is counted and never removed.
// See cliEntryHoldsHome.
func pruneCLI(cliDir string, keep int, newCLI string) {
	ents, err := os.ReadDir(cliDir)
	if err != nil {
		return
	}
	keep = max(keep, 0)
	type ver struct {
		name string
		mod  int64
	}
	var vs []ver
	for _, e := range ents {
		fi, err := os.Lstat(filepath.Join(cliDir, e.Name()))
		if err == nil && fi.Mode().IsRegular() && isCLIVersionFile(e.Name(), fi.Mode()) {
			vs = append(vs, ver{e.Name(), fi.ModTime().UnixNano()})
		}
	}
	// os.ReadDir gives the entries in name order, and the sort is stable.
	sort.SliceStable(vs, func(i, j int) bool { return vs[i].mod > vs[j].mod })
	for i := keep; i < len(vs); i++ {
		if vs[i].name == newCLI {
			continue
		}
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

// isCLIVersionFile reports whether a regular file of the cli-dir counts in the
// prune. On Linux and macOS it is a version name with an exec bit. On
// 5fd08069 "1.1.0" with mode 0644 does not count (cells L4 and M1). On Windows it is
// "<version>.exe" at any mode: a bare "1.1.0" and "README.exe" do not count
// (cell W3).
func isCLIVersionFile(name string, mode os.FileMode) bool {
	if cliExeSuffix != "" {
		v, ok := strings.CutSuffix(name, cliExeSuffix)
		return ok && isVersionName(v)
	}
	return isVersionName(name) && mode&0o111 != 0
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
