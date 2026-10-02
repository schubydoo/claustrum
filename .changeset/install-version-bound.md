---
default: minor
---

`-install` stops the direct `--version` run at 30 s (120 s after an install), as `89cb6289` does. It gains the default CLI folder and `<version>.exe` on Windows. It replaces a file at the `-cli-dir` path. `-cli-probe-timeout` is ignored.
