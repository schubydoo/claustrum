package main

import (
	"encoding/json"
	"testing"
)

// streamFrame uses a pointer ExitCode so a stdout/stderr frame omits the key
// entirely while an exit frame emits exitCode:0 for a clean exit. Data is the
// mirror image (present on data frames, omitted on exit).
func TestStreamFrameMarshaling(t *testing.T) {
	zero := 0
	negOne := -1
	cases := []struct {
		name string
		f    streamFrame
		want string
	}{
		{"stdout", streamFrame{Type: "stream", ProcessID: "p1", Stream: "stdout", Seq: 1, Data: "AAA"},
			`{"type":"stream","processId":"p1","stream":"stdout","seq":1,"data":"AAA"}`},
		{"exit zero", streamFrame{Type: "stream", ProcessID: "p1", Stream: "exit", Seq: 2, ExitCode: &zero},
			`{"type":"stream","processId":"p1","stream":"exit","seq":2,"exitCode":0}`},
		// 4534d86: a killed exit adds signal + killedBy after exitCode. Both are
		// omitempty, so the "exit zero" case above stays byte-identical. Windows emits
		// killedBy without signal (no signal on the wait path).
		{"exit killed", streamFrame{Type: "stream", ProcessID: "p1", Stream: "exit", Seq: 2, ExitCode: &negOne, Signal: "SIGTERM", KilledBy: "client"},
			`{"type":"stream","processId":"p1","stream":"exit","seq":2,"exitCode":-1,"signal":"SIGTERM","killedBy":"client"}`},
		{"exit killed no signal", streamFrame{Type: "stream", ProcessID: "p1", Stream: "exit", Seq: 2, ExitCode: &negOne, KilledBy: "shutdown"},
			`{"type":"stream","processId":"p1","stream":"exit","seq":2,"exitCode":-1,"killedBy":"shutdown"}`},
		// lineBytes is the replay buffer's accounting unit and must never reach
		// the wire. It is unexported today, so encoding/json drops it — but every
		// other case here leaves it at its zero value, which would keep passing if
		// someone exported it WITH omitempty. process.reattach re-marshals buffered
		// frames, and those DO carry a non-zero value, so that edit would silently
		// add a key to every replayed frame while this test stayed green.
		{"lineBytes never serialized",
			streamFrame{Type: "stream", ProcessID: "p1", Stream: "stdout", Seq: 3, Data: "AAA", lineBytes: 81},
			`{"type":"stream","processId":"p1","stream":"stdout","seq":3,"data":"AAA"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.f)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if got := string(b); got != tc.want {
				t.Errorf("\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}
