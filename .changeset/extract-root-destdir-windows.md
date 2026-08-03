---
default: patch
---

`files.extract_tar` now refuses a Windows volume or UNC share root as `destDir` — previously only the literal `/` was refused, so `C:\` passed the guard and reached the `os.RemoveAll` that clears the destination before extracting.
