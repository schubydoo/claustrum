---
default: patch
---

Login-shell PATH extraction now gives up after 4 seconds instead of 10, matching the reference, so a slow login shell no longer delays the first `process.spawn` or hands spawned children a PATH the reference would not have applied.
