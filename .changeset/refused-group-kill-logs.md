---
default: patch
---

After a refused group SIGKILL the daemon now logs the launcher and unattended lines of 87375cc7 and counts no refused sweep SIGKILL in the cleanup line. When the drain kill of a plain child took the claim, the shutdown wait now ends at its reap.
