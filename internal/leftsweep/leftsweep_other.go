//go:build !linux

package leftsweep

// Run ends nothing off Linux and reports no process (claustrum's own, not measured).
func Run(runDir string) (int, error) { return 0, nil }
