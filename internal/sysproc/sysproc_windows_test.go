//go:build windows

package sysproc

import (
	"syscall"
	"testing"
)

func TestNewSysProcAttrNewProcessGroup(t *testing.T) {
	attr := NewSysProcAttr()
	if attr == nil {
		t.Fatal("NewSysProcAttr returned nil")
	}
	if attr.CreationFlags&syscall.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Errorf("CreationFlags = %#x, want CREATE_NEW_PROCESS_GROUP set", attr.CreationFlags)
	}
}
