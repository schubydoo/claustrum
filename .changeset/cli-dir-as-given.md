---
default: patch
---

`-install` now replaces a regular file, a symlink to a regular file or a FIFO at the `-cli-dir` path, and removes a regular file named `ccd-cli-version` beside the cli-dir. On Linux and macOS it makes the folders of `-cli-dir` as given, so `D/sub/..` makes `D/sub`.
