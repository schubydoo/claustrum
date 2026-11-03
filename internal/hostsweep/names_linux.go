//go:build linux

package hostsweep

import "github.com/schubydoo/claustrum/internal/procinfo"

// The linux name of internal/procinfo under the name that the code of this package uses.

func procRoot() string { return procinfo.Root() }
