---
default: minor
---

On Linux and macOS a daemon with no authenticated request other than `server.capabilities` for 30 days, and no client connected, now shuts down. On Windows the daemon no longer exits when its socket file is gone.
