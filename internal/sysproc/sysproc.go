// Package sysproc holds the helpers that start, signal and reap a child process,
// read its exit signal, build its environment and cap its error output.
package sysproc

import (
	"os"
	"strconv"
	"strings"
)

// CloseAll closes every file, ignoring errors. Used on the spawn error paths and
// to force a stalled drain to end. Closing an *os.File twice is safe — the second
// call returns ErrClosed without touching the descriptor — so callers may close
// defensively without tracking whether a pump got there first. A nil file is safe
// too: Close of a nil *os.File returns ErrInvalid.
func CloseAll(fs ...*os.File) {
	for _, f := range fs {
		_ = f.Close()
	}
}

// RemoveEnvKey returns a copy of env with all entries whose key equals k removed.
func RemoveEnvKey(env []string, k string) []string {
	prefix := k + "="
	out := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}

func ReplaceOrAppendEnv(env []string, key, val string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			env[i] = prefix + val
			return env
		}
	}
	return append(env, prefix+val)
}

// CappedText keeps the first Max bytes written to it and counts the rest. Its text
// is the kept bytes, then "\n[… N more bytes]" when some were dropped (measured with
// 10000 bytes of stderr). claustrum's choices (not measured): the head is kept, not
// the tail, and the "\n" is added even when the kept part ends in a newline.
type CappedText struct {
	Max     int
	buf     []byte
	dropped int64
}

func (c *CappedText) Write(p []byte) (int, error) {
	room := c.Max - len(c.buf)
	if room > len(p) {
		room = len(p)
	}
	if room > 0 {
		c.buf = append(c.buf, p[:room]...)
	} else {
		room = 0
	}
	c.dropped += int64(len(p) - room)
	return len(p), nil
}

// Kept is the kept bytes alone, with no count of the dropped ones.
func (c *CappedText) Kept() []byte { return c.buf }

func (c *CappedText) String() string {
	if c.dropped == 0 {
		return string(c.buf)
	}
	return string(c.buf) + "\n[… " + strconv.FormatInt(c.dropped, 10) + " more bytes]"
}
