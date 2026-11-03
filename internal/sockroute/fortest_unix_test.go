//go:build unix

package sockroute

import (
	"errors"
	"os"
	"reflect"
	"testing"
)

// SetChangeBackForTest sets the seam and returns the one before it, so a test that
// wraps the old one and puts it back cannot recurse.
func TestSetChangeBackForTestReturnsThePrevious(t *testing.T) {
	stub := func(*os.File) error { return errors.New("stub") }
	before := reflect.ValueOf(changeBack).Pointer()
	old := SetChangeBackForTest(stub)
	if reflect.ValueOf(old).Pointer() != before {
		t.Errorf("SetChangeBackForTest returned another func than the seam before it")
	}
	if reflect.ValueOf(changeBack).Pointer() != reflect.ValueOf(stub).Pointer() {
		t.Errorf("SetChangeBackForTest did not set the seam")
	}
	if got := SetChangeBackForTest(old); reflect.ValueOf(got).Pointer() != reflect.ValueOf(stub).Pointer() {
		t.Errorf("the second SetChangeBackForTest did not return the stub")
	}
	if reflect.ValueOf(changeBack).Pointer() != before {
		t.Errorf("SetChangeBackForTest(old) did not put the seam back")
	}
}
