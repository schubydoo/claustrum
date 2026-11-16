package git

// failedCheckoutKillsGroup reports that a read-tree call that failed by itself
// gets one group SIGKILL after its exit (runCheckoutPipes). 4c111e86 sends it
// after a failed checkout (measured on Linux).
const failedCheckoutKillsGroup = true
