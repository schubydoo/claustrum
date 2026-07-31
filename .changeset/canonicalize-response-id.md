---
default: patch
---

A reply's `id` is now the request's id decoded and re-encoded rather than the raw bytes echoed back, so a non-integer id such as `1.0`, `1e2` or `{"b":1,"a":2}` comes back canonicalized exactly as the reference returns it.
