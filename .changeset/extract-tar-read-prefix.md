---
default: patch
---

`files.extract_tar` answers as `89cb6289` does for an archive that fails after its gzip header: `tar read: <text>` for an entry that cannot be read (it was `gzip: <text>`), `write <entry>: <text>` for a file whose content cannot be copied (it had no prefix), and `fileCount` 0 in both. Measured on a Linux VM, and the first-entry `tar read:` case also on macOS and Windows VMs.
