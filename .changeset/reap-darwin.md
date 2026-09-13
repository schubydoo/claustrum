---
default: minor
---

The startup orphan reap now runs on darwin. It was linux-only before. On darwin it reads a live process with `ps`, because darwin has no `/proc`. For each recorded child, it matches the live process to the record: same UTC start-time, process group leader, program, run-dir marker and direct-child marker. If every point matches, it ends the process group. So it never ends an unrelated process. This matches reference build `19f30c46` and was validated on a macOS VM.
