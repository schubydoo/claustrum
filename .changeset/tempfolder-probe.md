---
default: patch
---

On Linux and macOS a child no longer inherits a CLAUDE_CODE_TMPDIR, TMPDIR, TMP or TEMP whose folder cannot take a file. A missing folder is tested in its nearest existing parent, and the daemon logs one line for each drop.
