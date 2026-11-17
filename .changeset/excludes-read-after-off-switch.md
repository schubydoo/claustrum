---
default: patch
---

A git request after one with `"sessionFoldersOff":true` reads the global excludes setting again and no longer uses the value read under the switch.
