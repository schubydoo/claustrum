---
default: patch
---

A `process.spawn` with `"gitRead":true` accepts `git diff --no-color HEAD`, which it refused before.
