//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// An invalid UTF-8 byte in the resolved path stays a byte in the refusal and is printed
// \xff. JSON cannot carry the byte in the request, so the request names an ASCII alias
// and the check resolves it. Linux only: macOS file systems refuse the name. Measured
// side by side against f6010b97 on a Linux VM.
func TestGitDirTrustInvalidUTF8Operand(t *testing.T) {
	requireGit(t)
	base := realTempDir(t)
	T := filepath.Join(base, "bad\xffname", "T")
	initTrustMain(t, T)
	writeFile(t, filepath.Join(T, ".git", "commondir"), ".\n", 0o644)
	alias := filepath.Join(base, "L")
	if err := os.Symlink(T, alias); err != nil {
		t.Fatal(err)
	}
	want := wantTrustPrefix + `"` + base + `/bad\xffname/T/.git/commondir"` + wantM1Tail
	wantRPCError(t, "info(L)", info(t, alias), want)
}
