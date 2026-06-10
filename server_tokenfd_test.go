package main

import (
	"io"
	"os"
	"testing"
)

// readTokenFD reads the auth token from an open fd and normalizes it exactly like
// the -token-file path (trailing newline stripped, leading/interior spaces kept),
// so -token-fd and -token-file accept byte-for-byte the same token bytes.
func TestReadTokenFD(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	// A small write fits the pipe buffer, so this won't block before the read.
	if _, err := io.WriteString(w, "  s3kret-token\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = w.Close() // give the read an EOF

	got, err := readTokenFD(int(r.Fd()))
	if err != nil {
		t.Fatalf("readTokenFD: %v", err)
	}
	if want := "  s3kret-token"; got != want {
		t.Errorf("readTokenFD = %q, want %q", got, want)
	}
}
