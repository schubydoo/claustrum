---
default: patch
---

On Linux and macOS the daemon drops `CLAUDE_SSH_CHILD` and `CLAUDE_SSH_RUN_DIR` of the launcher from its own environment and ends it with `CLAUDE_SSH_DAEMON_CHILD=1`.
