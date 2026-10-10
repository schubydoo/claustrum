---
default: patch
---

`files.extract_tar` prepares `destDir` through a handle of its parent folder, with the failure texts of `5fd08069`. New refusals: a `destDir` that is home by identity, and on Windows a `\\?\` path whose last name ends in a dot or a space.
