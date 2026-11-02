package files

import (
	"encoding/json"
	"log"
	"testing"
)

// readRealCalls sends each files.read request to Handle and returns the
// replies.
func readRealCalls(t *testing.T, _ string, params []map[string]any) []json.RawMessage {
	t.Helper()
	got := make([]json.RawMessage, len(params))
	for i, p := range params {
		got[i] = json.RawMessage(dispatchRaw(t, newTestServer(t), rpcLine(t, "files.read", p)))
	}
	return got
}

// captureLog redirects the stdlib default logger into a buffer for the duration
// of fn.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf syncBuffer
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldW); log.SetFlags(oldF) })
	fn()
	return buf.String()
}

func filesReadRealAvailable() bool { return ReadRealAvailable() }

func logFilesReadRealStart() { LogReadRealStart() }
