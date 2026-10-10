---
default: patch
---

`files.extract_tar` prepares `destDir` through a handle of its parent folder. The failure texts of that step follow `5fd08069`, and a parent folder that cannot be opened leaves `destDir` as it is.
