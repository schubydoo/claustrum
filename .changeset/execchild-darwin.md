---
default: minor
---

On darwin the daemon now launches `process.spawn` children under a run-shaped socket through the exec-child trampoline, which was linux-only before. The trampoline stamps `CLAUDE_SSH_CHILD=<pid>:<start>` and `CLAUDE_SSH_RUN_DIR` on the child, matching reference build `19f30c46`. The darwin start-time token is the process start as a UTC ANSIC timestamp, so it matches the child registry record. This is off-wire and was validated on a macOS VM.
