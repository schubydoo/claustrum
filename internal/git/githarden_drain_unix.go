//go:build unix

package git

// failedCheckoutSkipsStdoutDrain reports that a read-tree call with a
// timeoutMs that failed by itself gets no stdout drain (runCheckoutPipes).
// That follows be1c1ca5 and b3081b51 after a failed smudge filter (measured
// on Linux and macOS).
const failedCheckoutSkipsStdoutDrain = true
