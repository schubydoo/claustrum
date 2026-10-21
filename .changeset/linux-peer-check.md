---
default: minor
---

On Linux with `CLAUDE_SSH_PEER_CHECK=1` the daemon now refuses a caller in other namespaces with a `-32006` frame, as `5fd08069` does. On Linux it answers `"peerCheck":"on"` and `peerCheckBy`, as `5fd08069` does. Every OS lists the `git.shadow_dir` feature, as `5fd08069` does.
