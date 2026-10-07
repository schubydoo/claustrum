---
default: patch
---

The `-cli-keep` prune of `-install` now counts folders too, as `89cb6289` does. Empty folders go, `-cli-keep 0` prunes, and old `*.zst.part` entries are swept.
