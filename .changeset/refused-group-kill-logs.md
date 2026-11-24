---
default: patch
---

After a refused group SIGKILL the daemon now logs the unattended line of 87375cc7, and on Linux also its launcher line. Its cleanup line counts no child whose sweep SIGKILL was refused, unless the sweep sent it a SIGTERM first. When the drain kill of a plain child took the claim, the shutdown wait now ends at its reap.
