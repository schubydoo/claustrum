---
default: patch
---

The host cleaner ends an orphan group that carries its own CLAUDE_SSH_CHILD stamp. It sends the group SIGKILL of a gone leader only if a member is left after 3 s.
