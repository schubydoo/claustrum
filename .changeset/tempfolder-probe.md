---
default: patch
---

On Linux and macOS a spawned child no longer inherits a TMPDIR, TEMP or CLAUDE_CODE_TMPDIR whose folder cannot take a file, and the daemon logs one line for each.
