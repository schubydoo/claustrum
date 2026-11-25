//go:build unix

package git

import "testing"

// TestCheckoutSSHDoorRefusalAfterAGoodListingWindowsOrder runs the Windows
// test of a refused config read of the door with a good second listing on
// Linux and macOS, with doorRefusalAfterListing switched on.
func TestCheckoutSSHDoorRefusalAfterAGoodListingWindowsOrder(t *testing.T) {
	old := doorRefusalAfterListing
	t.Cleanup(func() { doorRefusalAfterListing = old })
	doorRefusalAfterListing = true
	checkDoorRefusalAfterGoodListing(t)
}
