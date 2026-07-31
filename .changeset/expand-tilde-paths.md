---
default: patch
---

Expand a leading `~` to the home directory on every path parameter, so `files.*`, `git.*` and `process.spawn` no longer fail or create a literal `~` directory.
