---
default: patch
---

With `CLAUDE_SSH_PEER_CHECK=1` on Linux, the first good read of the own namespaces after a failed one logs `[Server] serving only callers in ...`.
