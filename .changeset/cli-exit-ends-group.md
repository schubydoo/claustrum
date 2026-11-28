---
default: minor
---

On Linux and macOS the daemon now ends what an exited Claude Code child leaves in its process group and lists `process.cli_exit_ends_group`, as 13047a51 does, unless CLAUDE_SSH_LEAVE_GROUPS is 1.
