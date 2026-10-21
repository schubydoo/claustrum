---
default: minor
---

`server.capabilities` answers the `peerCheck` member and lists the `server.peer_check` feature, as `5fd08069` does. With `CLAUDE_SSH_PEER_CHECK=1` macOS and Windows answer `"unavailable"` and serve every caller. No child of the daemon inherits the variable.
