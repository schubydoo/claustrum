---
default: patch
---

`-install` now removes leftover `.fetch-*` download temporaries, which previously counted against `-cli-keep` and could delete every real CLI version, and it prunes only after an install that succeeded.
