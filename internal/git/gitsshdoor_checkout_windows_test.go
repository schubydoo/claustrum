package git

import "testing"

// TestCheckoutSSHDoorRefusalAfterAGoodListingOnWindows: a refused config read
// of the door with a good second listing (checkDoorRefusalAfterGoodListing).
func TestCheckoutSSHDoorRefusalAfterAGoodListingOnWindows(t *testing.T) {
	checkDoorRefusalAfterGoodListing(t)
}
