package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	if !fi.Mode().IsRegular() {
		return errResult(req.ID, codeInvalidParam, "files.read: not a regular file")
	}
	if p.MaxBytes > 0 && fi.Size() > p.MaxBytes {
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

func filesExtractTar(req *request) response {
	var p extractTarParams
	if bad := bindParams(req, &p); bad != nil {
		return *bad
	}
	if p.ArchivePath == "" || p.DestDir == "" {
		return errResult(req.ID, codeInvalidParam, "archivePath and destDir are required")
	}
	if !filepath.IsAbs(p.DestDir) || filepath.Clean(p.DestDir) == "/" {
		return okResult(req.ID, extractResult{Error: fmt.Sprintf("destDir must be an absolute, non-root path: %q", p.DestDir)})
	}
	count, err := extractTarGz(p.ArchivePath, p.DestDir)
	if err != nil {
		return okResult(req.ID, extractResult{Success: false, FileCount: count, Error: err.Error()})
	}
	return okResult(req.ID, extractResult{Success: true, FileCount: count})
}

// maxExtractBytes caps the total uncompressed bytes written by extractTarGz.
// A crafted archive can have a tiny compressed size but expand to fill a disk;
// this cap bounds the damage. Set to a small value in tests.
var maxExtractBytes int64 = 512 * 1024 * 1024

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
	if err := os.RemoveAll(destDir); err != nil {
		return 0, fmt.Errorf("clean destDir: %v", err)
	}
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return 0, fmt.Errorf("mkdir destDir: %v", err)
	}
	tr := tar.NewReader(gz)
	count := 0
	var totalWritten int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return count, gzipErr{err}
		}
		target := filepath.Join(destDir, hdr.Name)
		// Reject entries that would escape destDir ("zip slip"). filepath.Rel
		// returns a path beginning with ".." exactly when target lands outside
		// destDir; an in-bounds "../" (or the destDir itself, e.g. a "." entry)
		// resolves inside and is allowed, matching the reference. The reference
		// rejects an escaping archive with this exact error and fileCount 0 —
		// even when earlier safe entries were already written to disk. (This Rel
		// form is also what CodeQL recognizes as a go/zipslip sanitizer.)
		if rel, err := filepath.Rel(destDir, target); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
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
				return count, err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
			if err != nil {
				return count, err
			}
			n, err := io.Copy(out, io.LimitReader(tr, maxExtractBytes-totalWritten+1))
			totalWritten += n
			out.Close()
			if err != nil {
				return count, err
			}
			if totalWritten > maxExtractBytes {
				return count, fmt.Errorf("extraction size limit exceeded")
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
