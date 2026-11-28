---
default: minor
---

The bridge now checks the listener when CLAUDE_SSH_LISTENER_CHECK is set to a value other than 0 or empty, and it exits 79 when it refuses the listener, as build 13047a51 does.
