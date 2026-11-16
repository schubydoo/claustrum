//go:build !darwin

package install

// managedResolveFaultShown is false off macOS. A launcher.resolve refusal of the
// managed launcher carries no fault there (not measured: no row shows one).
const managedResolveFaultShown = false
