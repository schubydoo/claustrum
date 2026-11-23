---
default: minor
---

On Linux and macOS the daemon now ends what an exited launcher leaves in its process group, as 87375cc7 does, unless CLAUDE_SSH_LEAVE_GROUPS is 1, and lists launcher.exit_ends_group.
