package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

func filesStat(req *request) response {
	var p pathParams
	_ = decodeParams(req, &p)
	fi, err := os.Stat(p.Path)
	if err != nil {
		return okResult(req.ID, statResult{})
	}
	return okResult(req.ID, statResult{
		Exists: true, IsDir: fi.IsDir(), Size: fi.Size(), Mode: fi.Mode().String(),
	})
}

func filesList(req *request) response {
	var p pathParams
	_ = decodeParams(req, &p)
	ents, err := os.ReadDir(p.Path) // returns entries sorted by name
	if err != nil {
		return errResult(req.ID, codeInternal, err.Error())
	}
	out := make([]listEntry, 0, len(ents))
	for _, e := range ents {
		out = append(out, listEntry{
			Name:  e.Name(),
			Path:  filepath.Join(p.Path, e.Name()),
			IsDir: e.IsDir(),
		})
	}
	return okResult(req.ID, listResult{Entries: out})
}

func filesRead(req *request) response {
	var p pathParams
	_ = decodeParams(req, &p)
	fi, err := os.Stat(p.Path)
	if err != nil {
		return okResult(req.ID, readResult{})
	}
	if fi.IsDir() {
		return errResult(req.ID, codeInvalidParam, "files.read: path is a directory")
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
	_ = decodeParams(req, &p)
	fi, err := os.Stat(p.Path)
	if err != nil {
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
	_ = decodeParams(req, &p)
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

func extractTarGz(archivePath, destDir string) (int, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return 0, err
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
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return count, gzipErr{err}
		}
		target := filepath.Join(destDir, hdr.Name)
		// Reject entries that would escape destDir ("zip slip"). filepath.Join
		// has already cleaned target, so a prefix test against the cleaned
		// destDir catches "../" traversal (an absolute or in-bounds "../" entry
		// resolves inside and is allowed). The reference daemon rejects such an
		// archive with this exact error and fileCount 0 — even when earlier safe
		// entries were already written to disk.
		if cleanDest := filepath.Clean(destDir); target != cleanDest && !strings.HasPrefix(target, cleanDest+string(os.PathSeparator)) {
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
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return count, err
			}
			out.Close()
			count++
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
