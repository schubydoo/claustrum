---
default: minor
---

claustrum now launches `process.spawn` children under a `run/<clientId>/` socket through reference build `19f30c46`'s exec-child self-re-exec trampoline (linux), which stamps `CLAUDE_SSH_CHILD=<pid>:<startTicks>` and `CLAUDE_SSH_RUN_DIR` on the child as a stable, pid-reuse-safe identity in its environment and holds the Go-runtime env across the re-exec; it is off-wire (no JSON-RPC frame changes), leaves a missing or non-executable command's `-32603 fork/exec` error frame unchanged via the trampoline's exec-error pipe, and had its child-marker set and format verified against the reference on a VM.
