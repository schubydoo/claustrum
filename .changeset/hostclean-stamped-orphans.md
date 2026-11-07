---
default: patch
---

The host cleaner ends an orphan group that carries its own CLAUDE_SSH_CHILD stamp and names the run dir of this daemon in CLAUDE_SSH_RUN_DIR. It sends the group SIGKILL of a gone leader only if a member is left after 3 s.
