---
default: patch
---

On Linux a shutdown no longer waits 1 s for a child whose SIGKILL failed with EPERM. 87375cc7 logs its cleanup line at once there.
