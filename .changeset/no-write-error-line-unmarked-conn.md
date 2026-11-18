---
default: patch
---

A connection that never showed the token and sends a bad line no longer logs a failed write of its parse-error reply.
