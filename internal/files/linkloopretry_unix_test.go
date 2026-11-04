//go:build unix

package files

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// A real link loop keeps the reply of one try: the retry only adds time.
func TestLinkLoopRealLoopKeepsReply(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := os.Symlink(b, a); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(a, b); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t)
	for _, c := range []struct{ method, want string }{
		{"files.stat", `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":%s}}`},
		{"files.list", `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":%s}}`},
	} {
		op := "stat"
		if c.method == "files.list" {
			op = "open"
		}
		text, _ := json.Marshal(op + " " + a + ": too many levels of symbolic links")
		got := dispatchRaw(t, s, rpcLine(t, c.method, map[string]any{"path": a}))
		if want := fmt.Sprintf(c.want, string(text)); got != want {
			t.Errorf("%s of a link loop = %s, want %s", c.method, got, want)
		}
	}
}
