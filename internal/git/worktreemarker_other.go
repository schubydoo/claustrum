//go:build !linux

package git

import (
	"os"
	"path/filepath"
)

// markerCloseStops says whether a failed close of the new marker stops the
// create. Here the create goes on, as before (claustrum's own, not measured).
const markerCloseStops = false

// openMarkerFile makes the new marker in dir with O_EXCL and opens it for
// writing, by its path. release does nothing.
func openMarkerFile(dir string) (f *os.File, release func(), err error) {
	f, err = os.OpenFile(filepath.Join(dir, managedWorktreesMarker), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	return f, func() {}, err
}
