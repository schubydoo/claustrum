//go:build linux

package runlock

import "github.com/schubydoo/claustrum/internal/procinfo"

// The linux name of internal/procinfo under the name that the code of this package uses.

func osReadlink(name string) (string, error) { return procinfo.Readlink(name) }
