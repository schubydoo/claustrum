---
default: patch
---

Wait for the login-shell PATH before the first spawn

The first `process.spawn` built its child environment before the login-shell
PATH extraction finished, so it did not find binaries in `~/.local/bin` or nvm.
The environment build now waits for the extraction.
