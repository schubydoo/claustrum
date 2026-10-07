---
default: patch
---

The `-cli-keep` prune of `-install` now counts every cli-dir entry, as `89cb6289` does. Empty folders go, `-cli-keep 0` removes every entry, and old `*.zst.part` entries are swept.
