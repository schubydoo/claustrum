---
default: patch
---

With no core.excludesFile set, each git call looks up the default excludes file again, so a file made or removed while the daemon runs takes effect.
