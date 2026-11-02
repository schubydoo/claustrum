package files

import (
	"errors"
	"testing"
)

func TestGzipErrPrefix(t *testing.T) {
	if got := (gzipErr{errors.New("invalid header")}).Error(); got != "gzip: invalid header" {
		t.Errorf("gzipErr.Error() = %q, want %q", got, "gzip: invalid header")
	}
}
