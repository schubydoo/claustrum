---
default: patch
---

On Linux a process.spawn with cliTempRoot reads CLAUDE_CODE_TMPDIR, TMPDIR, TMP and TEMP, skips a launched spawn and answers within 2 s.
