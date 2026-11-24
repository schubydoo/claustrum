---
default: patch
---

After a refused group SIGKILL the daemon now logs the lines of 87375cc7 and counts no refused sweep SIGKILL in the cleanup line. It also ends the shutdown wait at the reap. Each rule follows 87375cc7 on Linux.
