//go:build unix

package main

import (
	"testing"
)

func TestDetachSysProcAttr(t *testing.T) {
	if detachSysProcAttr() == nil {
		t.Error("detachSysProcAttr should return a non-nil SysProcAttr")
	}
}
