//go:build !linux

package main

// hostCredsPipe is false on macOS and Windows, where 13047a51 refuses every hostCreds
// of the right form (measured).
const hostCredsPipe = false

// hostCredsFeatures is empty on macOS and Windows. 13047a51 lists no
// process.spawn.host_creds_pipe there (measured: cell w_e76_caps of run
// m25a-win11l-20261128T024832Z, and cell m_e76_caps of run b1-tahoe-20261128T024804Z).
var hostCredsFeatures []string
