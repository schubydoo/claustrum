---
default: patch
---

With `CLAUDE_SSH_PEER_CHECK=1` and no namespaces to compare, the daemon logs `[Server] found no namespaces to compare: cannot tell callers in a sandbox from others, serving all` at start.
