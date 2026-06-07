package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		// real binary prefixes the gzip error, yielding "gzip: gzip: invalid header"
		return 0, gzipErr{err}
	}
	defer gz.Close()
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return 0, err
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
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				return count, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return count, err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode))
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
	return count, nil
}

// gzipErr reproduces the real binary's "gzip: " prefix on archive errors.
type gzipErr struct{ inner error }

func (e gzipErr) Error() string { return "gzip: " + e.inner.Error() }
