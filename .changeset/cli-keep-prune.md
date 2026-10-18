---
default: patch
---

`-install` now removes a `<version>.zst.part` file of the cli-dir that is more than 7 days old, and `-cli-keep 0` prunes every older version file.
