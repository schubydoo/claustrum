---
default: patch
---

Wait for the login-shell PATH extraction before the first `process.spawn` builds its child environment, so the first command finds binaries in `~/.local/bin` and nvm.
