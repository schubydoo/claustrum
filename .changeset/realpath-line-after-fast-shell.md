---
default: patch
---

On macOS the files.realpath start line now waits a short time for the login shell read, so a fast shell logs its PATH line first, as 87375cc7 does in most runs.
