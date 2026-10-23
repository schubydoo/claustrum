---
default: patch
---

`files.extract_tar` now answers `tar read: <text>` for an entry that cannot be read (it was `gzip: <text>`) and `write <entry>: <text>` for a file whose content cannot be copied.
