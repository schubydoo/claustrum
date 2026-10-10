package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

func (s *server) handleFiles(req *request) response {
	var fn func(*request) response
	switch req.Method {
	case "files.list":
		fn = filesList
	case "files.stat":
		fn = filesStat
	case "files.read":
		fn = filesRead
	case "files.validate":
		fn = filesValidate
	case "files.extract_tar":
		fn = filesExtractTar
	default:
		return unknownMethod(req)
	}
	if bad := needParams(req); bad != nil {
		return *bad
	}
	return fn(req)
}

type pathParams struct {
	Path     string `json:"path"`
	MaxBytes int64  `json:"maxBytes"`
}

// defaultReadMaxBytes is the ceiling files.read applies when the caller supplies
// no usable maxBytes. Probe-measured against the reference at 5db5e4a: a
// 262144-byte file reads, 262145 fails with "file exceeds maxBytes".
const defaultReadMaxBytes = 262144

// statForRequest wraps os.Stat with the reference's error policy for files.stat
// and files.validate (files.read opens the path instead). A genuine ENOENT is the
// "does not exist" answer of each caller. Any OTHER stat failure is surfaced
// verbatim rather than being flattened into "does not exist".
//
// Probe-measured against the reference at 5db5e4a on 2026-07-30 — three triggers,
// all reachable:
//
//	<dir>/a.txt/../a.txt   ENOTDIR        "stat <p>: not a directory"
//	a 300-char name        ENAMETOOLONG   "stat <p>: file name too long"
//	a NUL byte in the path EINVAL         "stat <p>: invalid argument"
//
// ENOTDIR is the one that matters: it needs no adversarial input, only a client
// joining a path against a regular file. claustrum previously answered all three
// with exists:false, which told the caller the path was absent when the real
// problem was that the path was malformed or unusable.
func statForRequest(path string) (fs.FileInfo, error, bool) {
	fi, err := os.Stat(path)
	switch {
	case err == nil:
		return fi, nil, false
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil, true // genuinely absent
	default:
		return nil, err, false
	}
}

func filesStat(req *request) response {
	var p pathParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	fi, err, absent := statForRequest(p.Path)
	if err != nil {
		return errResult(req.ID, codeInternal, err.Error())
	}
	if absent {
		return okResult(req.ID, statResult{})
	}
	return okResult(req.ID, statResult{
		Exists: true, IsDir: fi.IsDir(), Size: fi.Size(), Mode: fi.Mode().String(),
	})
}

func filesList(req *request) response {
	var p pathParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	// Open with O_DIRECTORY (oDirectoryFlag) then read. On 7d193f89's files.list
	// a regular file, readable or not, reports `open <p>: not a directory` and an
	// unreadable directory reports `open <p>: permission denied`. Both are matched
	// byte-for-byte. Before 7d193f89 the reference said
	// `readdirent <p>: not a directory` instead. On Windows the flag is 0 (no
	// O_DIRECTORY) and this is os.Open, whose non-dir wording is not pinned there.
	f, err := os.OpenFile(p.Path, os.O_RDONLY|oDirectoryFlag, 0)
	if err != nil {
		return errResult(req.ID, codeInternal, err.Error())
	}
	ents, err := f.ReadDir(-1)
	_ = f.Close()
	if err != nil {
		return errResult(req.ID, codeInternal, err.Error())
	}
	// f.ReadDir returns raw directory order; os.ReadDir sorted for us. The
	// byte-wise name sort is verified parity with the reference, so restore it
	// explicitly — dropping it would silently break list ordering while fixing
	// an error string.
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	out := make([]listEntry, 0, len(ents))
	for _, e := range ents {
		// The reference omits hidden entries (any name beginning with ".",
		// e.g. .git/.env) from files.list — probe-confirmed against the
		// reference daemon. Match it so a workspace listing is byte-identical.
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		full := filepath.Join(p.Path, e.Name())
		// isDir comes from Stat, which FOLLOWS symlinks, not from the raw dirent
		// type. The reference output matches: a symlink to a directory reports
		// isDir:true, and a dangling symlink (Stat fails) reports isDir:false.
		isDir := false
		if fi, err := os.Stat(full); err == nil {
			isDir = fi.IsDir()
		}
		out = append(out, listEntry{Name: e.Name(), Path: full, IsDir: isDir})
	}
	return okResult(req.ID, listResult{Entries: out})
}

// filesRead opens the path first and tests the kind of the open file after it.
// 5fd08069 gives the same answers in the same order (Linux, macOS and Windows
// VMs): a path that cannot be
// opened keeps its `open <path>: ...` text. A FIFO writer that waits in its
// open returns at the request (Linux and macOS VMs). A file that is not regular and not a directory
// gets -32602 "files.read: not a regular file". On Linux and macOS the null
// device itself reads as empty content (isNullDevice). The open does not wait
// for a FIFO writer (readOpenFlag).
func filesRead(req *request) response {
	var p pathParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	f, err := os.OpenFile(p.Path, os.O_RDONLY|readOpenFlag, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return okResult(req.ID, readResult{})
	}
	if err != nil {
		return errResult(req.ID, codeInternal, err.Error())
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return errResult(req.ID, codeInternal, err.Error())
	}
	if fi.IsDir() {
		return errResult(req.ID, codeInvalidParam, "files.read: path is a directory")
	}
	if !fi.Mode().IsRegular() && !isNullDevice(fi) {
		return errResult(req.ID, codeInvalidParam, "files.read: not a regular file")
	}
	// An absent, zero, or negative maxBytes is NOT "no limit" — the reference
	// substitutes defaultReadMaxBytes and rejects anything larger, so the cap
	// applies to the default request shape. A positive maxBytes is honored
	// verbatim, above or below the default (probe-verified against 5db5e4a: a
	// 300000-byte file reads fine at maxBytes=10000000 but errors at 0 and -1).
	maxBytes := p.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultReadMaxBytes
	}
	if fi.Size() > maxBytes {
		return errResult(req.ID, codeInvalidParam, "files.read: file exceeds maxBytes")
	}
	// The buffer starts at the size of the stat. The read still goes to the end
	// of the file, because a file under /proc reports the size 0.
	buf := bytes.NewBuffer(make([]byte, 0, fi.Size()+1))
	if _, err := buf.ReadFrom(f); err != nil {
		return errResult(req.ID, codeInternal, err.Error())
	}
	return okResult(req.ID, readResult{Content: buf.String(), Exists: true})
}

func filesValidate(req *request) response {
	// params presence is enforced by handleFiles; an empty {} is accepted here
	// (path defaults to "" -> "Path does not exist").
	var p pathParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	fi, err, absent := statForRequest(p.Path)
	// Unlike stat/read, validate reports the failure in its own result shape
	// rather than as an RPC error — the stat text replaces "Path does not
	// exist" in the error field (probe-measured).
	if err != nil {
		return okResult(req.ID, validateResult{Error: err.Error()})
	}
	if absent {
		return okResult(req.ID, validateResult{Error: "Path does not exist"})
	}
	return okResult(req.ID, validateResult{Valid: true, IsDir: fi.IsDir()})
}

type extractTarParams struct {
	ArchivePath string `json:"archivePath"`
	DestDir     string `json:"destDir"`
}

// wipeDestDir is the recursive delete extract_tar performs before unpacking,
// behind a seam. It removes ONE name inside the handle of the parent folder of
// destDir: the last name of the cleaned destDir. The handle follows no link and
// no junction, so a link at that name or below it goes as a link. The handle
// refuses the names "." and "..", so the wipe cannot take the parent itself.
//
// The seam exists so that a test proving a destDir gate is actually WIRED INTO
// filesExtractTar can send the very input the gate exists to refuse. For
// isFilesystemRoot that input is a real filesystem root — on Windows, C:\\. For
// wipesHomeDir it is a home directory. If either guard stops holding, an
// unstubbed test would answer the question by deleting the CI runner or the
// developer's home. A test whose failure mode is destroying the machine is not
// a test, so the wipe is observable and stubbable instead: TestFilesExtractTarErrors
// and TestFilesExtractTarRefusesHomeDir record whether it was reached and never
// let it run.
//
// Production never reassigns it.
var wipeDestDir = func(parent *os.Root, name string) error { return parent.RemoveAll(name) }

// afterDestDirMkdir runs between the mkdir of destDir and its open, behind a
// seam. A test swaps destDir for a link there, to show that the swapped folder is
// refused. Production never reassigns it, and it does nothing.
var afterDestDirMkdir = func() {}

// lstatDestByPath reads the entry at the cleaned destDir as the system resolves
// the path, behind a seam. A test makes this view differ from the view of the
// parent handle. Production never reassigns it.
var lstatDestByPath = os.Lstat

// errDestDirHoldsHome is the home refusal of files.extract_tar. The text has the
// destDir of the request as it was sent.
func errDestDirHoldsHome(destDir string) error {
	return fmt.Errorf("destDir must not be or contain the home directory: %q", destDir)
}

// destEntryHoldsHome is the home guard of the entry that the wipe acts on. entry
// is the lstat answer for the last name of destDir in the handle of its parent
// folder, or nil for no entry. A real folder there is compared with the home
// folder and with each parent folder of home by identity (folderHoldsHome), so
// the spelling of the path does not matter. On Linux and macOS that covers a
// path through a link above the last name. On Windows it covers a path with
// the `\\?\` prefix and the short 8.3 name of home, which the text compare of
// wipesHomeDir lets pass (Windows VM, the test binary).
//
// A link or a junction at the name is not followed: the wipe removes the link
// itself, and its target stays.
func destEntryHoldsHome(cleanedDest string, entry os.FileInfo) bool {
	return entry != nil && entry.IsDir() && folderHoldsHome(cleanedDest, entry)
}

// parentNotSearchableError is the answer of checkDestViews for a parent folder
// whose handle gives a permission error for the name. Its text is the text of an
// open of the parent that fails, on Linux and macOS. On Windows this answer is
// not used: no row is measured there, and the lstat error keeps the text of a
// remove through the handle.
type parentNotSearchableError struct{ dir string }

func (e parentNotSearchableError) Error() string {
	return "open " + e.dir + ": permission denied"
}

// destViewFailure gives the failure of checkDestViews its prefix: step for the
// step that it guards, or "open parent" for a parent that cannot be searched.
func destViewFailure(step string, err error) error {
	if errors.As(err, &parentNotSearchableError{}) {
		step = "open parent"
	}
	return fmt.Errorf("%s: %v", step, err)
}

// errDestViewsDiffer is the answer of checkDestViews for two views that do not
// name one entry. The text is claustrum's own.
var errDestViewsDiffer = errors.New("is not the entry that the path names")

// checkDestViews tests that the cleaned path of destDir and the name in the
// handle of its parent folder name one entry. They agree if neither view has an
// entry, or if both have the same one. An lstat sees a link or a junction
// itself, in both views. A path view that cannot be read counts as no agreement.
//
// A name that the handle cannot read gets the text of a remove of that name
// through the handle, and no remove runs. On a Windows VM 5fd08069 answers
// "clean destDir: RemoveAll NUL: path escapes from parent" for the last name
// NUL, and "RemoveAll dest?: The filename, directory name, or volume label
// syntax is incorrect." for dest?. For a last name of dots and spaces alone,
// such as "...", it answers "RemoveAll ...: invalid argument". Nothing changes
// on disk there.
//
// A parent folder that the daemon opened and cannot search gives a permission
// error at that lstat. The answer is then the text of a parent that cannot be
// opened. 5fd08069 answers "open parent: open <parent>: permission denied" for
// a parent of mode 0400 or 0600, and changes nothing (Linux and macOS VMs).
//
// The first result is the entry at the name in the handle, or nil if the handle
// has none or cannot read it.
func checkDestViews(parent *os.Root, cleanedDest, destName string) (os.FileInfo, error) {
	byHandle, handleErr := parent.Lstat(destName)
	if runtime.GOOS != "windows" && errors.Is(handleErr, fs.ErrPermission) {
		return nil, parentNotSearchableError{dir: parent.Name()}
	}
	if handleErr != nil && !errors.Is(handleErr, fs.ErrNotExist) {
		var pathErr *os.PathError
		if errors.As(handleErr, &pathErr) {
			handleErr = pathErr.Err
		}
		return nil, &os.PathError{Op: "RemoveAll", Path: destName, Err: handleErr}
	}
	if handleErr != nil {
		byHandle = nil
	}
	byPath, pathErr := lstatDestByPath(cleanedDest)
	switch {
	case pathErr != nil && !errors.Is(pathErr, fs.ErrNotExist):
	case (pathErr == nil) != (handleErr == nil):
	case pathErr == nil && !os.SameFile(byPath, byHandle):
	default:
		return byHandle, nil
	}
	return byHandle, fmt.Errorf("%q %w", destName, errDestViewsDiffer)
}

// isFilesystemRoot reports whether p names a filesystem root, on any platform.
//
// The gate this backs matters because filesExtractTar WIPES destDir before
// extracting. The wipe removes the last name of destDir as a tree, through a
// handle of the parent folder. A root has no parent and no last name.
//
// Whether the reference refuses a root destDir is NOT measured — an earlier
// version of this comment asserted "the reference has no such guard", which is
// an absence claim with no probe behind it, and
// docs/protocol/files-extract-tar.md files the refusal as neither parity nor
// divergence. What is certain is the consequence
// here: the steps after this guard take a parent folder and a last name from
// destDir, so the guard must not have a platform-shaped hole whatever the
// reference does.
//
// It used to compare `filepath.Clean(destDir) == "/"`. That is a Unix-only
// notion of root: a Windows volume root cleans to `C:\`, never the string "/",
// and filepath.IsAbs accepts it — so `C:\` (and a UNC share root) passed the
// gate and reached the RemoveAll. Raised in review on #224 as pre-existing.
//
// `filepath.Dir(x) == x` is the platform's own definition of "has no parent",
// true for "/" on Unix and for a drive or UNC root on Windows, and it needs no
// separate spelling per platform. Clean first so a trailing separator ("/" vs
// "//", `C:\` vs `C:\\`) cannot slip past by shape.
func isFilesystemRoot(p string) bool {
	c := filepath.Clean(p)
	return filepath.Dir(c) == c
}

func filesExtractTar(req *request) response {
	var p extractTarParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	if p.ArchivePath == "" || p.DestDir == "" {
		return errResult(req.ID, codeInvalidParam, "archivePath and destDir are required")
	}
	if !filepath.IsAbs(p.DestDir) || isFilesystemRoot(p.DestDir) {
		return okResult(req.ID, extractResult{Error: fmt.Sprintf("destDir must be an absolute, non-root path: %q", p.DestDir)})
	}
	// A home directory is absolute and is not a filesystem root, so it clears the
	// gate above and reaches the wipe. `"destDir":"~"` expands to exactly that
	// before this function is entered, which is how an in-repo fuzzer deleted the
	// maintainer's home directory on 2026-08-02. Refused here, alongside the root
	// check, so neither reaches extractTarGz: both errors precede the archive
	// open, so the archive is not consumed either. See homeguard.go for why
	// containment is the test and why ~/... stays allowed.
	if wipesHomeDir(p.DestDir) {
		return okResult(req.ID, extractResult{Error: errDestDirHoldsHome(p.DestDir).Error()})
	}
	count, err := extractTarGz(p.ArchivePath, p.DestDir)
	if err != nil {
		return okResult(req.ID, extractResult{Success: false, FileCount: count, Error: err.Error()})
	}
	return okResult(req.ID, extractResult{Success: true, FileCount: count})
}

// maxExtractBytes caps the total uncompressed bytes written by extractTarGz.
// A crafted archive can have a tiny compressed size but expand to fill a disk;
// the cap bounds that damage.
//
// ZERO (the default) DISABLES IT, which is what the reference does at every size
// the probe could reach: measured, a 629 MB archive extracts fully there and
// answers {"success":true,"fileCount":1}, while a capped claustrum answered an
// error the reference never produces at that size. The
// cap shipped on by default at 512 MiB and that was a live user-facing break —
// Claude Desktop owns the argv, so a caller who hit it had no way through. It is
// now opt-in: divergence D3, set via -max-extract-bytes or the max-extract-bytes
// key in claustrum.conf. Also set directly by tests.
var maxExtractBytes int64

// cappedCopy writes one archive entry's body to out, honoring the maxExtractBytes
// cap when it is set. It returns the bytes written; the caller applies the
// over-cap check against the running totalWritten. Extracted from extractTarGz so
// the saturating cap arithmetic lives in one named, testable place.
//
// maxExtractBytes <= 0 (the default) copies straight through. It is deliberately
// NOT a LimitReader with a huge bound, since the max-total+1 arithmetic below is
// what defines the boundary behaviour and routing the unlimited case through it
// invents a boundary.
// (Both paths read identically today: archive/tar's Reader.WriteTo is unexported,
// so io.Copy falls through to the same 32 KiB generic copy either way. If a future
// Go exports it, only the uncapped branch takes the sparse-file fast path — same
// bytes, but no longer the same code path. Re-measure if that lands.)
//
// The +1 is the boundary definition: reading one byte PAST the cap is what lets
// the caller's totalWritten > maxExtractBytes test fire at all. It must SATURATE
// rather than wrap — Go wraps signed overflow, so at maxExtractBytes == MaxInt64
// (reachable since the cap became settable) the sum would become MinInt64,
// LimitReader would EOF on the first Read, io.Copy would not report it, every
// entry would be created at 0 bytes, totalWritten would stay 0 so the cap test
// never fires, and the reply would be success:true over a destDir of empty files.
// Measured: the bound wraps to -9223372036854775808 and io.Copy returns 0.
// (`TestFilesExtractTarCapMaxInt64DoesNotOverflow` pins this.)
func cappedCopy(out io.Writer, tr io.Reader, totalWritten int64) (int64, error) {
	if maxExtractBytes <= 0 {
		return io.Copy(out, tr)
	}
	bound := maxExtractBytes - totalWritten
	if bound < math.MaxInt64 {
		bound++
	}
	return io.Copy(out, io.LimitReader(tr, bound))
}

func extractTarGz(archivePath, destDir string) (int, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		// The reference prefixes this one open failure with "open archive: ",
		// distinguishing it from the per-entry write errors below.
		return 0, fmt.Errorf("open archive: %w", err)
	}
	// The reference consumes the source archive: once opened, archivePath is
	// removed on every outcome — success, bad gzip, or unsafe path alike
	// (probe-verified). Declared before the Close defer so the fd closes first,
	// keeping the unlink safe on Windows.
	defer os.Remove(archivePath)
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		// real binary prefixes the gzip error, yielding "gzip: gzip: invalid header"
		return 0, gzipErr{err}
	}
	defer gz.Close()
	// The preparation of destDir answers as 5fd08069 does in each measured row.
	// The order of its failure texts rests on Linux and macOS VM rows. The
	// steps below are claustrum's. Each step runs
	// only AFTER the gzip header validates above, so a missing archive or a file
	// that is not gzip leaves an old destDir as it is (89cb6289 and 5fd08069,
	// Linux, macOS and Windows VMs). filesExtractTar ran the root test and
	// wipesHomeDir on destDir before this function, so no step runs for a
	// destDir that they refuse.
	//
	//  1. The missing folders above destDir are made, each with mode 0700 less
	//     the umask. A failure answers "mkdir parent: mkdir <path>: <reason>",
	//     with the whole path of the folder that fails, and nothing changes on
	//     disk (89cb6289 and 5fd08069, Linux and macOS VMs). The older text "mkdir parent
	//     <entry>: " in the entry loop has the name of an archive entry.
	//  2. The parent folder of destDir is opened as a handle. A failure answers
	//     "open parent: open <parent>: <reason>", and destDir stays as it is
	//     (parent modes 0300, 0100 and 0000, Linux and macOS VMs).
	//  3. The wipe removes the last name of destDir through that handle, as a
	//     tree. A failure answers "clean destDir: RemoveAll <name>: <reason>"
	//     (Linux, macOS and Windows VMs). The entries that can go are gone then.
	//  4. A new folder with that name is made through the handle, with mode 0700.
	//     A failure answers "mkdir destDir: mkdirat <name>: <reason>" (parent
	//     mode 0500, Linux and macOS VMs).
	//  5. destDir is opened through the handle and tested (below).
	//
	// filesExtractTar refused a file system root, so the cleaned destDir has a
	// parent and a last name. filepath.Clean leaves no "." and no ".." as the
	// last name of an absolute path, and the handle refuses both names.
	//
	// Each step below takes this one cleaned form of destDir: the parent path,
	// the last name, the lstat of the identity test and the texts. No call that
	// resolves a path gets the raw path of the request.
	cleanedDest := filepath.Clean(destDir)
	parentDir, destName := filepath.Dir(cleanedDest), filepath.Base(cleanedDest)
	if err := os.MkdirAll(parentDir, 0o700); err != nil {
		return 0, fmt.Errorf("mkdir parent: %v", err)
	}
	parentRoot, err := os.OpenRoot(parentDir)
	if err != nil {
		return 0, fmt.Errorf("open parent: %v", err)
	}
	defer parentRoot.Close()
	// The wipe may remove only the entry that the cleaned path of the request
	// names by itself. The handle and the system can read one name in two ways.
	// On Windows the handle drops a dot or a space after a name, and a path with
	// the `\\?\` prefix keeps it. The name in the handle is then a sibling that
	// the path does not name, and wipesHomeDir judged the path. So the two views
	// are compared before the wipe. If they differ, nothing is deleted, and no
	// destDir is made. The folders above destDir from the first step stay. This
	// test and its text are claustrum's own. Both reference builds extract
	// there (Windows VM).
	//
	// The entry at the name gets the home guard by identity first. A folder that
	// is the home folder, or a parent folder of it, gets the home refusal of
	// this method. The archive is gone then, because it was opened.
	entry, viewErr := checkDestViews(parentRoot, cleanedDest, destName)
	if destEntryHoldsHome(cleanedDest, entry) {
		return 0, errDestDirHoldsHome(destDir)
	}
	if viewErr != nil {
		return 0, destViewFailure("clean destDir", viewErr)
	}
	if err := wipeDestDir(parentRoot, destName); err != nil {
		return 0, fmt.Errorf("clean destDir: %v", err)
	}
	if err := parentRoot.Mkdir(destName, 0o700); err != nil {
		return 0, fmt.Errorf("mkdir destDir: %v", err)
	}
	// The same test for the new folder. With no entry in either view before the
	// wipe, the mkdir through the handle can still make a folder that the path
	// does not name. If the two views differ there, the entry at that name goes
	// with a plain remove through the handle, never as a tree. The home guard by
	// identity comes before that remove, and no remove runs after it.
	entry, viewErr = checkDestViews(parentRoot, cleanedDest, destName)
	if destEntryHoldsHome(cleanedDest, entry) {
		return 0, errDestDirHoldsHome(destDir)
	}
	if viewErr != nil {
		_ = parentRoot.Remove(destName)
		return 0, destViewFailure("mkdir destDir", viewErr)
	}
	afterDestDirMkdir()
	// claustrum creates the parent folders and the files of the archive through
	// a handle of destDir. Its texts for the two entry collisions below are then
	// those of 5fd08069, with a name relative to destDir in place of the whole
	// path.
	//
	// A destDir that cannot be searched gets the answer of 5fd08069 here, before
	// the first entry. Cell 2a1-modes-u0100 (Linux VM) and cell 2a-modes-u100
	// (macOS VM) run the daemon with umask 0100, so destDir gets mode 0600.
	// 5fd08069 answers there:
	//
	//	open destDir: "dest" could not be examined (statat .: permission denied)
	//
	// With a daemon umask of 0400 the new destDir has mode 0300, and 5fd08069
	// answers (Linux and macOS VMs):
	//
	//	open destDir: openat dest: permission denied
	destRoot, err := parentRoot.OpenRoot(destName)
	if err != nil {
		return 0, fmt.Errorf("open destDir: %v", err)
	}
	defer destRoot.Close()
	opened, err := destRoot.Stat(".")
	if err != nil {
		return 0, fmt.Errorf("open destDir: %q could not be examined (%v)", destName, err)
	}
	// The parent is opened by path, and wipesHomeDir judged destDir as text.
	// Another process can put a link or a junction at destDir between the wipe
	// and the open. The open through the parent refuses a link that leaves the
	// parent, and it follows one that stays inside. The handle then names a
	// folder that the guard did not judge, and the entries and the marker remove
	// act on it. So destDir must still be a real
	// directory, and it must be the folder of the handle. If not, the request
	// fails here, before any entry is written and before any remove. This test
	// and its text are claustrum's own, and no reference build is measured there.
	//
	// The lstat takes the cleaned path. With a separator after the last name,
	// an lstat follows a link at that name and the test passes behind it.
	if judged, err := os.Lstat(cleanedDest); err != nil || !judged.IsDir() || !os.SameFile(judged, opened) {
		return 0, fmt.Errorf("open destDir: %q changed while it was opened", destName)
	}
	tr := tar.NewReader(gz)
	count := 0
	var totalWritten int64

	// Zip-slip guard operands, hoisted: both are loop-invariant, and computing
	// them per entry invited the reading that they depend on hdr.Name.
	//
	// destPrefix appends the separator UNLESS cleanedDest already ends in one,
	// which happens only for a root destDir ("/", or a Windows volume root).
	// Concatenating unconditionally yields "//" there, and no target has that
	// prefix, so every entry would be rejected — a differential against the old
	// filepath.Rel form caught exactly this, on 19 pairs, all with destDir "/".
	//
	// The prefix form also rejects everything when destDir cleans to "." (a
	// relative destDir), where the Rel form accepted. That is unreachable rather
	// than handled: filesExtractTar gates on filepath.IsAbs(destDir) before this
	// runs, so a relative destDir never gets here. Named because it is the one
	// input on which the two forms genuinely disagree — if that gate is ever
	// relaxed, this guard has to be revisited with it.
	destPrefix := cleanedDest
	if !strings.HasSuffix(destPrefix, string(os.PathSeparator)) {
		destPrefix += string(os.PathSeparator)
	}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			// 89cb6289 answers "tar read: <text>" with fileCount 0 when the tar reader
			// cannot read the next entry. For gzip data that is not a tar that is
			// measured on Linux, macOS and Windows VMs. For a later entry, also after
			// files were written, it is measured on a Linux VM.
			return 0, fmt.Errorf("tar read: %v", err)
		}
		// Reject entries that would escape destDir ("zip slip"). filepath.Join
		// cleans, so target is already normalized; an entry then lands inside
		// destDir exactly when target IS destDir or sits under it with a
		// separator. A name that passes this test can still be unsafe: the
		// second test below refuses a name that leaves destDir and comes back.
		// The reference rejects an escaping archive with this exact error and
		// fileCount 0 — even when earlier safe entries were already written.
		//
		// THE TRAILING SEPARATOR IS THE WHOLE GUARD. Comparing against cleanedDest
		// alone would admit a sibling whose name merely starts with destDir's —
		// "../sub-sibling.txt" out of a "…/sub" destDir — which is the classic
		// way this check is written wrong. TestFilesExtractTarZipSlipShapes has a
		// row for exactly that, and it is the only test in the suite that catches
		// it; do not "simplify" the separator away.
		//
		// This replaced an equivalent filepath.Rel form on 2026-08-03. The Rel
		// version was correct and stayed byte-identical from the commit CodeQL
		// marked as fixing go/zipslip (896fd5c) — but CodeQL 2.26.2 stopped
		// recognizing it as a sanitizer and reopened the alert with no code
		// change. This form is the one its query models. Behaviour is unchanged,
		// which the shapes table is there to prove rather than assert.
		//
		// cleanedDest/destPrefix are computed once above the loop.
		target := filepath.Join(cleanedDest, hdr.Name)
		if target != cleanedDest && !strings.HasPrefix(target, destPrefix) {
			return 0, fmt.Errorf("unsafe path in archive: %s", hdr.Name)
		}
		// A name that leaves destDir and comes back is unsafe too, although it
		// resolves inside. 5fd08069 answers the same text for `../dest/x.txt`,
		// `sub/../../dest/y.txt`, the directory entry `../dest/dd/` and
		// `sub/../../dest` (Linux, macOS and Windows VMs).
		// 89cb6289 extracts the three first names. On a Windows VM both builds
		// refuse the names `NUL` and `a:b` with this text.
		//
		// The rule is lexical: the name without its leading separators must be a
		// local path (filepath.IsLocal). `a/../b.txt`, `./c.txt`, `.` and an
		// absolute name pass, as on 5fd08069. A name of separators alone passes
		// as the name `.` does. A directory entry `/` succeeds on 5fd08069 (Linux
		// VM). A file entry `/` gets its `create /: openat .:` text there (Linux
		// and Windows VMs).
		if !entryNameStaysInside(hdr.Name) {
			return 0, fmt.Errorf("unsafe path in archive: %s", hdr.Name)
		}
		// The modes are owner-only and fixed. A directory is 0700. A file is
		// 0700 if its archive mode has the execute bit of the owner, and 0600 if
		// it does not. 5fd08069 gives those modes for the archive modes 0644,
		// 0600, 0444, 0755, 0700, 0111, 04755, 02755 and 01777 (Linux and macOS
		// VMs). The modes 0010, 0001, 0011, 0654, 0645, 0610, 0601 and 0674 give
		// 0600 there, and 0100 and 0744 give 0700 (Linux and macOS VMs). No
		// setuid, setgid or sticky bit arrives. 89cb6289 gives each file 0600.
		//
		// inDest is the place of the entry below destDir. The zip-slip test above
		// passed, so inDest is a clean relative path with no ".." part.
		inDest := "."
		if target != cleanedDest {
			inDest = strings.TrimPrefix(target, destPrefix)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if inDest != "." {
				if err := destRoot.MkdirAll(inDest, 0o700); err != nil {
					// "mkdir <entry>: <text>" with fileCount 0, also when an earlier
					// entry is on disk. A file entry `a` and then a directory entry
					// give on 5fd08069:
					//
					//	mkdir a/: mkdirat a: file exists           (Linux, macOS and Windows VMs)
					//	mkdir a/b/: openat a: not a directory      (Linux and macOS VMs)
					//
					// 89cb6289 has the prefix and the fileCount 0 too.
					return 0, fmt.Errorf("mkdir %s: %v", hdr.Name, err)
				}
			}
		case tar.TypeReg:
			if parent := filepath.Dir(inDest); parent != "." {
				if err := destRoot.MkdirAll(parent, 0o700); err != nil {
					// "mkdir parent <entry>: <text>" with fileCount 0, also when an
					// earlier entry is on disk. A file entry `a` and then an entry
					// `a/b` give on 5fd08069 (Linux, macOS and Windows VMs):
					//
					//	mkdir parent a/b: mkdirat a: file exists
					//
					// The blocker comes from the archive itself, so the wipe of
					// destDir cannot remove it.
					return 0, fmt.Errorf("mkdir parent %s: %v", hdr.Name, err)
				}
			}
			perm := os.FileMode(0o600)
			if hdr.Mode&0o100 != 0 {
				perm = 0o700
			}
			// "create <entry>: <text>" with fileCount 0. The prefix names the
			// archive entry. A directory entry `d` and then a file entry `d` give
			// on 5fd08069:
			//
			//	create d: openat d: file exists      (Linux and macOS VMs)
			//	create d: openat d: is a directory   (Windows VM)
			//
			// The exclusive create of claustrum through the handle gives both texts.
			out, err := destRoot.OpenFile(inDest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
			if errors.Is(err, fs.ErrExist) {
				// A second file entry with the name of an earlier one replaces the
				// file. The new file has the mode of this entry: on 5fd08069 the
				// modes 0644 and then 0755 give 0700, and 0755 and then 0644 give
				// 0600 (Linux and macOS VMs). It has the name of this entry too:
				// `a.txt` and then `A.TXT` leave `A.TXT` on 5fd08069 (Windows VM).
				//
				// The remove is a plain remove of one name through the handle of
				// destDir, never a tree. It takes a regular file inside destDir
				// and nothing else. A directory, a link or a FIFO at the name
				// keeps the "file exists" answer.
				if fi, statErr := destRoot.Lstat(inDest); statErr == nil && fi.Mode().IsRegular() {
					if err = destRoot.Remove(inDest); err == nil {
						out, err = destRoot.OpenFile(inDest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
					}
				}
			}
			if err != nil {
				return 0, fmt.Errorf("create %s: %v", hdr.Name, err)
			}
			n, err := cappedCopy(out, tr, totalWritten)
			totalWritten += n
			out.Close()
			if err != nil {
				// 89cb6289 answers "write <entry>: <text>" with fileCount 0 when the
				// content of a file cannot be copied, for example from an archive
				// that is cut inside the file (Linux VM). A failed write of the
				// extracted file takes this arm too, and that case is not measured.
				return 0, fmt.Errorf("write %s: %v", hdr.Name, err)
			}
			if maxExtractBytes > 0 && totalWritten > maxExtractBytes {
				// The entry that tripped the cap was written truncated (the
				// LimitReader stops at cap+1), so remove it rather than leaving a
				// corrupt file behind that looks like a partial success.
				// The remove is a plain remove of one name through the handle of
				// destDir, never a tree.
				_ = destRoot.Remove(inDest)
				// fileCount 0, not the partial count — grouping this with the
				// arms that reject the archive outright (create, mkdir-parent,
				// mkdir, write, zip-slip, unsupported type), all of which answer
				// 0. It is NOT every arm: a failed write of .synced still returns
				// the partial count, and the cap arm was simply in the wrong group.
				return 0, fmt.Errorf("extraction size limit exceeded")
			}
			count++
		default:
			// The reference supports only regular files and directories; any
			// other entry (symlink=2, hardlink=1, device, fifo, …) aborts the
			// whole extraction with fileCount 0. %c prints the tar typeflag as
			// its character ("2"), matching the reference's wording.
			return 0, fmt.Errorf("unsupported tar entry type %c: %s", hdr.Typeflag, hdr.Name)
		}
	}
	if err := writeSyncedMarker(destRoot); err != nil {
		// A marker that cannot be written is not measured on a reference build,
		// and neither is an entry at .synced that cannot be removed.
		return count, fmt.Errorf("write .synced: %v", err)
	}
	return count, nil
}

// entryNameStaysInside reports whether the name of an archive entry stays inside
// destDir at each step. The test is lexical. It cuts the leading separators, so an
// absolute name passes and lands under destDir.
func entryNameStaysInside(name string) bool {
	local := strings.TrimLeftFunc(name, func(r rune) bool { return r == '/' || r == filepath.Separator })
	return local == "" || filepath.IsLocal(local)
}

// writeSyncedMarker puts the empty marker file .synced at the top of destDir. It
// is not counted in fileCount.
//
// Whatever sits at that name goes first, as a tree. After the entries of the
// archive, 89cb6289 and 5fd08069 leave an empty file there (Linux, macOS and
// Windows VMs). That holds for a directory entry `.synced/` with files and
// folders below it, and for a file entry with content. The file has mode 0600
// (Linux and macOS VMs).
//
// The remove is a recursive delete of the fixed name .synced, and the write is an
// exclusive create of that name. Both go through the handle of destDir, so neither
// follows a link out of destDir. extractTarGz tested that handle against the
// folder that wipesHomeDir judged. In a normal run the remove takes the entry of
// this archive. Do not build the name from a parameter or from the archive.
func writeSyncedMarker(destRoot *os.Root) error {
	if err := destRoot.RemoveAll(".synced"); err != nil {
		return err
	}
	marker, err := destRoot.OpenFile(".synced", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return marker.Close()
}

// gzipErr reproduces the real binary's "gzip: " prefix on a gzip header that
// cannot be read.
type gzipErr struct{ inner error }

func (e gzipErr) Error() string { return "gzip: " + e.inner.Error() }
