package files

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"testing"

	"github.com/schubydoo/claustrum/internal/rpc"
)

// The test helpers of package main that the tests of this package use, under
// the same names.

const testToken = "s3cret-token"

// wireRequest is one request line as the tests write it.
type wireRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Auth    string          `json:"auth"`
}

// server stands in for the daemon of package main. dispatchRaw gives a line to
// Handle.
type server struct{}

func newTestServer(tb testing.TB) *server {
	tb.Helper()
	return &server{}
}

// dispatchRaw decodes a raw request line, gives it to Handle and returns the
// marshaled response.
func dispatchRaw(t *testing.T, _ *server, line string) string {
	t.Helper()
	var req wireRequest
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	resp := Handle(&rpc.Request{JSONRPC: req.JSONRPC, ID: req.ID, Method: req.Method, Params: req.Params, Auth: req.Auth})
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	return string(b)
}

// rpcLine builds a single authenticated JSON-RPC request line. params is
// marshaled as-is (pass a map or struct); a nil params is sent as `{}` so the
// needParams presence gate is satisfied.
func rpcLine(t *testing.T, method string, params interface{}) string {
	t.Helper()
	if params == nil {
		params = struct{}{}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	req := wireRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  method,
		Params:  raw,
		Auth:    testToken,
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return string(b)
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil { // defeat umask for a stable stat mode
		t.Fatal(err)
	}
}

func makeTarGz(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names) // deterministic archive order
	for _, n := range names {
		body := files[n]
		if err := tw.WriteHeader(&tar.Header{
			Name: n, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}

// syncBuffer is the locked sink of captureLog and captureLogBuf. The test
// goroutine reads it while other goroutines can still write to the logger.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogBuf points the default logger at a locked buffer, with no timestamp,
// until the test ends, and returns the buffer.
func captureLogBuf(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldW); log.SetFlags(oldF) })
	return buf
}

// homeEnvVar is the variable that os.UserHomeDir reads on this platform.
func homeEnvVar() string {
	if runtime.GOOS == "windows" {
		return "USERPROFILE"
	}
	return "HOME"
}
