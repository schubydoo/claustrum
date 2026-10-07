---
default: patch
---

The `-cli-keep` prune of `-install` now counts folders too, as `89cb6289` does. `-cli-keep 0` prunes, old `*.zst.part` entries are swept, and both passes skip the home folder.
