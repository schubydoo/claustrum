---
default: minor
---

On linux, claustrum now launches `process.spawn` children under a `run/<clientId>/` socket through the exec-child self-re-exec trampoline of reference build `19f30c46`. The trampoline stamps `CLAUDE_SSH_CHILD=<pid>:<startTicks>` and `CLAUDE_SSH_RUN_DIR` on the child as a stable, pid-reuse-safe identity in its environment. It holds the Go-runtime env across the re-exec. The change is off-wire (no JSON-RPC frame changes). The `-32603 fork/exec` error frame for a missing or non-executable command stays unchanged through the exec-error pipe of the trampoline. The child-marker set and format were verified against the reference on a VM.
