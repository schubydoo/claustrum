---
default: patch
---

On Linux and macOS, git.status with a regular file inside a repository as baseRepo now asks git about that repository, and a failure of that call answers its error.
