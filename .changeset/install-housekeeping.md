---
default: patch
---

`-install` now accepts a `-cli-version` that starts with `.blob-`. A download that is no zstd archive gets the decoder text, a rename error has the `rename: ` prefix, and on Windows a denied rename is tried again.
