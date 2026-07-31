---
default: patch
---

`files.read` now applies the reference's 256 KiB (262144-byte) default cap when `maxBytes` is absent, zero, or negative, instead of reading the file without any limit; an explicit positive `maxBytes` is still honored verbatim.
