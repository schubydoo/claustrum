// Package hostsweep holds the host cleaner of -serve. It ends orphaned process
// groups, retires abandoned daemons and tidies their stale run dirs. Windows
// links no cleaner.
package hostsweep

// Start starts the host cleaner for the daemon at socket (see startHostCleaner).
func Start(socket string) { startHostCleaner(socket) }
