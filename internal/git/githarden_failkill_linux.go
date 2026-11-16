package git

// failedCheckoutKillsGroup reports that a read-tree call that failed by itself
// gets one group SIGKILL after its exit (runCheckoutPipes). 4c111e86 sends it
// after a Git LFS download error or a failed smudge filter, also with a
// timeoutMs (measured on Linux). Other failures are claustrum's own, not measured.
const failedCheckoutKillsGroup = true
