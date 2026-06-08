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
			f.CliWasPresent = true
		} else if err := ensureCLI(o, f.CliPath); err != nil {
			f.CliError = err.Error()
		}
		if o.cliKeep > 0 {
			pruneCLI(o.cliDir, o.cliKeep)
		}
	}

	b, _ := json.Marshal(f)
	fmt.Printf("__INSTALL_RESULT__%s\n", b)
}

// ensureCLI materializes the CLI binary from a .zst blob: either an already
// uploaded one (-cli-zst, SFTP fallback) or a download (-cli-url), verified
// against -cli-checksum, then zstd-decompressed to cliPath.
func ensureCLI(o installOpts, cliPath string) error {
	var zst []byte
	var err error
	switch {
	case o.cliZst != "":
		// SFTP fallback: the blob arrives over an already-authenticated channel,
		// so the reference does NOT verify -cli-checksum on this path.
		if zst, err = os.ReadFile(o.cliZst); err != nil {
			return fmt.Errorf("opening input: %v", err)
		}
	case o.cliURL != "":
		if zst, err = httpGet(o.cliURL); err != nil {
			return fmt.Errorf("download failed: %v", err)
		}
		// Downloads are verified UNCONDITIONALLY — an empty -cli-checksum still
		// fails ("expected= , actual=<sha>") — matching the reference.
		sum := sha256.Sum256(zst)
		if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, o.cliChecksum) {
			return fmt.Errorf("checksum mismatch: expected=%s, actual=%s", o.cliChecksum, got)
		}
	default:
		return fmt.Errorf("cli %s missing and no --cli-url or --cli-zst provided", o.cliVersion)
	}
	if err := os.MkdirAll(filepath.Dir(cliPath), 0o755); err != nil {
		return err
	}
	if err := zstdDecompress(zst, cliPath); err != nil {
		return fmt.Errorf("decompressing: %v", err)
	}
	if err := os.Chmod(cliPath, 0o755); err != nil {
		return err
	}
	// Verify the extracted CLI actually runs; if not, remove it and report.
	if !isRunnable(cliPath) {
		_ = os.Remove(cliPath)
		return fmt.Errorf("installed cli at %s is not runnable", cliPath)
	}
	// SFTP fallback: the uploaded .zst blob is consumed on success.
	if o.cliZst != "" {
		_ = os.Remove(o.cliZst)
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
	_, err = io.Copy(out, dec)
	return err
}

func httpGet(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// pruneCLI keeps the most-recent `keep` version files under cliDir (by mtime).
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

func detectLibc() string {
	out, err := exec.Command("ldd", "--version").CombinedOutput()
	return classifyLibc(out, err, os.Stat)
}

// classifyLibc maps an `ldd --version` result to "musl" or "glibc". It is split
// from detectLibc with an injectable stat so the ldd-failure + musl-marker
// branches are testable on any host — a glibc box can't otherwise reach them.
func classifyLibc(lddOut []byte, lddErr error, stat func(string) (os.FileInfo, error)) string {
	if lddErr != nil {
		if _, e := stat("/lib/ld-musl-x86_64.so.1"); e == nil {
			return "musl"
		}
		return "glibc"
	}
	if strings.Contains(strings.ToLower(string(lddOut)), "musl") {
		return "musl"
	}
	return "glibc"
}
