---
default: minor
---

`server.capabilities` answers the `peerCheck` member and lists the `server.peer_check` feature, as `5fd08069` does. With `CLAUDE_SSH_PEER_CHECK=1` claustrum answers `"unavailable"` and serves every caller. On Linux `5fd08069` answers `"on"` there (D25). No child of the daemon inherits the variable.
