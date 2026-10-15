---
default: minor
---

On Linux and macOS a daemon with no counted request for 30 days and no client connected now shuts down. A socket that is lost while a client is connected no longer gives the "no longer leads" line after that client leaves, as on `5fd08069`. On Windows the daemon no longer exits when its socket file is gone, as on `5fd08069`.
