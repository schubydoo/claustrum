---
default: patch
---

On Linux and Windows a failed `process.spawn` of a busy program file now gives the reason `busy`, and a file that is not a Windows program gives `not_executable`, as 87375cc7 does.
