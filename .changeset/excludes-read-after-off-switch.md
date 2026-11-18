---
default: patch
---

With no excludes value stored before, a git request after one with `"sessionFoldersOff":true` reads the global excludes setting again. It no longer uses the value read under the switch.
