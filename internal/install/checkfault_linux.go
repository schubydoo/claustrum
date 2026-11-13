package install

// checkFaultShown is true on Linux, where a spawn refusal with no launcherCheck
// carries a launcherFault (c9125eb8, measured on Linux).
const checkFaultShown = true
