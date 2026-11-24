---
default: patch
---

On Linux, after a refused group SIGKILL the daemon now logs the launcher and unattended lines of 87375cc7. Its cleanup line counts no child whose sweep SIGKILL was refused, unless the sweep sent it a SIGTERM first. When the drain kill of a plain child took the claim, the shutdown wait now ends at its reap.
