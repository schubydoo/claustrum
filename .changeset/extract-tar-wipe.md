---
default: patch
---

On Windows `files.extract_tar` now refuses a `\\?\` `destDir` whose last name ends in a dot or a space.
