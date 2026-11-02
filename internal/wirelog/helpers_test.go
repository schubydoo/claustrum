package wirelog

import (
	"bytes"
	"log"
	"testing"
)

// captureLog redirects the stdlib default logger into a buffer for the duration
// of fn.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldW); log.SetFlags(oldF) })
	fn()
	return buf.String()
}
