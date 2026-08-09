package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
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

// statForRequest wraps os.Stat with the reference's error policy: a genuine
// ENOENT is the "does not exist" answer each caller reports in its own shape,
// while any OTHER stat failure is surfaced verbatim rather than being flattened
// into "does not exist".
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
	// Open then read, rather than os.ReadDir, to match the reference's error
	// text and call order. os.ReadDir collapses both failures into an "open"
	// PathError, so a regular file reported `open <p>: not a directory` where
	// the reference says `readdirent <p>: not a directory`, and an unreadable
	// file reported `not a directory` where the reference says
	// `permission denied`. Splitting the calls reproduces both.
	f, err := os.Open(p.Path)
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
		// The reference resolves isDir via Stat (FOLLOWING symlinks), not the raw
		// dirent type — so a symlink to a directory reports isDir:true, and a
		// dangling symlink (Stat fails) reports isDir:false.
		isDir := false
		if fi, err := os.Stat(full); err == nil {
			isDir = fi.IsDir()
		}
		out = append(out, listEntry{Name: e.Name(), Path: full, IsDir: isDir})
	}
	return okResult(req.ID, listResult{Entries: out})
}

// filesReadRegularOnly makes filesRead refuse anything that is not a regular
// file. It bounds two real hazards: a read of a FIFO with no writer blocks in
// open — holding a request goroutine AND a descriptor, since linux reserves the
// fd number before blocking — and os.ReadFile on /dev/zero or /dev/urandom never
// reaches EOF, so the daemon grows until the kernel OOM-kills it.
//
// FALSE (the default) DISABLES IT, which is what the reference does. Measured: it
// reads /dev/null and answers {"content":"","exists":true} where a guarded
// claustrum answers -32602, and it replies normally the instant a FIFO's writer
// opens rather than refusing the read. The guard shipped on by default in PR 56
// and Claude Desktop owns the -serve argv, so a caller reading a character device
// had no way through. It is now opt-in: divergence D4, set via
// -files-read-regular-only or the files-read-regular-only key in claustrum.conf.
// Also set directly by tests.
//
// ⚠️ The two hazards are the COST of turning it off, and they are the reference's
// behavior, not a claustrum regression — the same trade the other five flips make.
// Do not "improve" the off path by narrowing the predicate to permit only some
// character devices: /dev/null would pass and so would /dev/zero and /dev/random,
// which is the unbounded read. On or off, whole — that is why this is a flag and
// not a smarter check.
var filesReadRegularOnly bool

func filesRead(req *request) response {
	var p pathParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	fi, err, absent := statForRequest(p.Path)
	if err != nil {
		return errResult(req.ID, codeInternal, err.Error())
	}
	if absent {
		return okResult(req.ID, readResult{})
	}
	if fi.IsDir() {
		return errResult(req.ID, codeInvalidParam, "files.read: path is a directory")
	}
	// D4 FLIP: opted in only. Off (the default), the predicate is not evaluated at
	// all and the read proceeds exactly as the reference's does — including the
	// blocking and unbounded cases documented on the var above.
	if filesReadRegularOnly && !fi.Mode().IsRegular() {
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
	b, err := os.ReadFile(p.Path)
	if err != nil {
		return errResult(req.ID, codeInternal, err.Error())
	}
	return okResult(req.ID, readResult{Content: string(b), Exists: true})
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
// behind a seam.
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
var wipeDestDir = os.RemoveAll

// isFilesystemRoot reports whether p names a filesystem root, on any platform.
//
// The gate this backs matters because filesExtractTar WIPES destDir before
// extracting (os.RemoveAll), so a root destDir would recursively delete the
// volume.
//
// Whether the reference refuses a root destDir is NOT measured — an earlier
// version of this comment asserted "the reference has no such guard", which is
// an absence claim with no probe behind it, and docs/PROTOCOL.md files the
// refusal as neither parity nor divergence. What is certain is the consequence
// here: this guard is the only thing between a root destDir and a recursive
// delete, so it must not have a platform-shaped hole whatever the reference
// does.
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
		return okResult(req.ID, extractResult{Error: fmt.Sprintf("destDir must not be or contain the home directory: %q", p.DestDir)})
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
// maxExtractBytes <= 0 (the default) copies straight through, exactly as the
// reference does — deliberately NOT a LimitReader with a huge bound, since the
// max-total+1 arithmetic below is what defines the boundary behaviour and routing
// the unlimited case through it would invent a boundary the reference has none of.
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
	// The reference daemon makes extraction idempotent: it wipes destDir and
	// recreates it before unpacking. Both steps run only AFTER the gzip header
	// validates above, so a corrupt archive leaves an existing destDir intact
	// (probe-verified). destDir is created owner-only (0700), matching the
	// reference's umask-077 extraction.
	if err := wipeDestDir(destDir); err != nil {
		return 0, fmt.Errorf("clean destDir: %v", err)
	}
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return 0, fmt.Errorf("mkdir destDir: %v", err)
	}
	tr := tar.NewReader(gz)
	count := 0
	var totalWritten int64

	// Zip-slip guard operands, hoisted: both are loop-invariant, and computing
	// them per entry invited the reading that they depend on hdr.Name.
	//
	// destPrefix appends the separator UNLESS cleanDest already ends in one,
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
	cleanDest := filepath.Clean(destDir)
	destPrefix := cleanDest
	if !strings.HasSuffix(destPrefix, string(os.PathSeparator)) {
		destPrefix += string(os.PathSeparator)
	}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return count, gzipErr{err}
		}
		// Reject entries that would escape destDir ("zip slip"). filepath.Join
		// cleans, so target is already normalized; an entry then lands inside
		// destDir exactly when target IS destDir or sits under it with a
		// separator. An in-bounds "../" (and the destDir itself, e.g. a "."
		// entry) resolves inside and is allowed, matching the reference. The
		// reference rejects an escaping archive with this exact error and
		// fileCount 0 — even when earlier safe entries were already written.
		//
		// THE TRAILING SEPARATOR IS THE WHOLE GUARD. Comparing against cleanDest
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
		// cleanDest/destPrefix are computed once above the loop.
		target := filepath.Join(cleanDest, hdr.Name)
		if target != cleanDest && !strings.HasPrefix(target, destPrefix) {
			return 0, fmt.Errorf("unsafe path in archive: %s", hdr.Name)
		}
		// The reference ignores the archive's mode bits and forces owner-only
		// fixed modes: every extracted directory is 0700 and every file 0600
		// (an executable 0755 entry still lands 0600 — probe-verified).
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return count, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				// "mkdir parent <entry>: " — a DIFFERENT prefix from the create
				// path below, and fileCount 0 rather than the partial count.
				// Measured against 5db5e4a with an archive whose FIRST entry
				// writes a regular file "p" and whose second needs "p" as a
				// directory:
				//
				//	reference : mkdir parent p/child.txt: mkdir <dest>/p: not a directory
				//	            fileCount 0
				//	claustrum : mkdir <dest>/p: not a directory
				//	            fileCount 1
				//
				// fileCount 0 despite one entry already being on disk is the same
				// shape the zip-slip rejection has: the reference reports nothing
				// extracted even when earlier entries were written.
				//
				// An earlier version of this comment said the mkdir path was "not
				// provokable" because extract_tar wipes destDir before extracting.
				// That was wrong — the wipe cannot remove a blocker the ARCHIVE
				// ITSELF creates on an earlier entry, which is how this was
				// measured. Raised in review.
				return 0, fmt.Errorf("mkdir parent %s: %v", hdr.Name, err)
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
			if err != nil {
				// "create <entry>: " prefix, naming the ARCHIVE ENTRY rather than
				// the resolved target. Measured against 5db5e4a with an entry that
				// lands on an existing directory:
				//
				//	reference : create ../sub: open <dest>: is a directory
				//	claustrum : open <dest>: is a directory
				//
				// This lands in the extract_tar error field on the wire.
				//
				// The MkdirAll above carries a DIFFERENT prefix ("mkdir parent
				// <entry>: ") and is handled there — measured, after an earlier
				// version of this comment wrongly called it unprovokable.
				//
				// The io.Copy failure below is still unmeasured: it needs a short
				// read the harness cannot stage, so it is left bare rather than
				// wrapped on the strength of the two arms that WERE measured.
				// Assuming a third prefix from two observations is how a parity
				// claim outruns its evidence.
				// fileCount 0, not the partial count — measured with an archive
				// whose first entry succeeds and whose second hits this branch:
				// the reference answers fileCount 0 while claustrum answered 1.
				// Same shape as the mkdir-parent and zip-slip arms.
				return 0, fmt.Errorf("create %s: %v", hdr.Name, err)
			}
			n, err := cappedCopy(out, tr, totalWritten)
			totalWritten += n
			out.Close()
			if err != nil {
				return count, err
			}
			if maxExtractBytes > 0 && totalWritten > maxExtractBytes {
				// The entry that tripped the cap was written truncated (the
				// LimitReader stops at cap+1), so remove it rather than leaving a
				// corrupt file behind that looks like a partial success.
				_ = os.Remove(target)
				// fileCount 0, not the partial count — grouping this with the four
				// arms that reject the archive outright (create, mkdir-parent,
				// zip-slip, unsupported type), all of which answer 0. It is NOT
				// every arm: mid-stream tr.Next, TypeDir mkdir, io.Copy and
				// write .synced all still return the partial count, and the cap
				// arm was simply in the wrong group.
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
	// On success the reference drops an empty ".synced" marker at destDir root
	// (not counted in fileCount); a write failure surfaces as this error.
	if err := os.WriteFile(filepath.Join(destDir, ".synced"), nil, 0o600); err != nil {
		return count, fmt.Errorf("write .synced: %v", err)
	}
	return count, nil
}

// gzipErr reproduces the real binary's "gzip: " prefix on archive errors.
type gzipErr struct{ inner error }

func (e gzipErr) Error() string { return "gzip: " + e.inner.Error() }
