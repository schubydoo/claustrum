package rpc

import (
	"encoding/json"
	"testing"
)

// TestResponseNoFrameIsNeverEncoded: NoFrame is exported so that package main
// can set it, and its tag keeps it out of the frame. A set NoFrame leaves the
// encoding as it is without the field.
func TestResponseNoFrameIsNeverEncoded(t *testing.T) {
	r := OKResult(json.RawMessage(`7`), PongResult{Pong: true})
	r.NoFrame = true
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `{"jsonrpc":"2.0","id":7,"result":{"pong":true}}`; string(b) != want {
		t.Errorf("\n got: %s\nwant: %s", b, want)
	}
}
