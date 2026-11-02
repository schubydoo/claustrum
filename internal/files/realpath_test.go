package files

import (
	"encoding/json"
	"fmt"
	"testing"
)

// TestRealpathFailureNoUsableName pins the bytes of the no_usable_name reply on
// every system. Only the Windows walk returns errNoUsableName, so this is the one
// test of the frame that runs on Linux and macOS too.
func TestRealpathFailureNoUsableName(t *testing.T) {
	resp := realpathFailure(json.RawMessage(`1`), fmt.Errorf("walk: %w", errNoUsableName))
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"files.realpath: walk: no verified path for the target (it may be deleted or moved, or on Windows on a network share)","data":{"reason":"no_usable_name"}}}`
	if string(b) != want {
		t.Errorf("reply\n got %s\nwant %s", b, want)
	}
}
