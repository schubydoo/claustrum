---
default: patch
---

The `-cli-keep` prune of `-install` now counts folders too, as `89cb6289` does. `-cli-keep 0` prunes, a negative value exits 2, and old `*.zst.part` entries are swept.
