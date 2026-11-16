//go:build darwin

package install

// managedResolveFaultShown is true on macOS: a launcher.resolve refusal of the
// managed launcher carries a fault (4c111e86, measured on macOS).
const managedResolveFaultShown = true
