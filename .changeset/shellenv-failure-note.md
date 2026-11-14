---
default: patch
---

On Linux and macOS the failure line of the login-shell PATH read says that programs keep the last PATH that was read, or sshd's if none was, also before a good read.
