---
default: patch
---

The host cleaner ends an orphan group with its own CLAUDE_SSH_CHILD stamp and any CLAUDE_SSH_RUN_DIR entry of a run dir under its root. It sends the group SIGKILL of a gone leader only if a member is left after 3 s.
