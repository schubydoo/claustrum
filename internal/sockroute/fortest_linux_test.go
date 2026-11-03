package sockroute

import "testing"

// SetProcSelfFDForTest sets the folder and returns the one before it.
func TestSetProcSelfFDForTestReturnsThePrevious(t *testing.T) {
	before := procSelfFD
	old := SetProcSelfFDForTest("/none")
	if old != before || procSelfFD != "/none" {
		t.Errorf("old %q, now %q: want %q, \"/none\"", old, procSelfFD, before)
	}
	if got := SetProcSelfFDForTest(old); got != "/none" || procSelfFD != before {
		t.Errorf("restore returned %q, now %q: want \"/none\", %q", got, procSelfFD, before)
	}
}
