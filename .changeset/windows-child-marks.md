---
default: patch
---

On Windows the daemon and a plain `process.spawn` child no longer get `CLAUDE_SSH_CHILD` and `CLAUDE_SSH_RUN_DIR`, as 87375cc7 and 13047a51 do.
