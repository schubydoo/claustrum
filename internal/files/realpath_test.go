package files

import (
	"encoding/json"
	"fmt"
	"testing"
)

// TestRealpathFailureNoUsableName pins the bytes of the no_usable_name reply on
// every system: stoppedShort after a link, atLetter for the drive of the path
// itself (87375cc7, measured on Windows). Only the Windows walk returns
// errNoUsableName, so this is the one test of the frame that runs on Linux and
// macOS too.
func TestRealpathFailureNoUsableName(t *testing.T) {
	const msg = `"message":"files.realpath: walk: no verified path for the target (it may be deleted or moved, or on Windows on a network share)"`
	for _, c := range []struct {
		err  error
		data string
	}{
		{errNoUsableName, `{"reason":"no_usable_name","stoppedShort":true}`},
		{errNoUsableNameAtLetter, `{"reason":"no_usable_name","atLetter":"share"}`},
	} {
		b, err := json.Marshal(realpathFailure(json.RawMessage(`1`), fmt.Errorf("walk: %w", c.err)))
		if err != nil {
			t.Fatal(err)
		}
		if want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,` + msg + `,"data":` + c.data + `}}`; string(b) != want {
			t.Errorf("reply\n got %s\nwant %s", b, want)
		}
	}
}
