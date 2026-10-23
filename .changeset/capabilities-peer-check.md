---
default: minor
---

`server.capabilities` now reports the `peerCheck` state and the `server.peer_check` feature. With `CLAUDE_SSH_PEER_CHECK=1` on Linux the daemon answers `"peerCheck":"on"` with a `peerCheckBy` member, and refuses a caller in other namespaces with a `-32006` frame. macOS and Windows answer `"unavailable"` and serve every caller. No child of the daemon gets the variable.
