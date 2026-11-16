---
default: patch
---

The peer check polls the pidfd of a caller again after `EINTR`, and the log line of a failed poll names the caller by its pid.
