---
default: minor
---

`-install` now follows the reference: it stops `<cli> --version` at 30 s (120 s after an install), prunes only old CLI version files, removes `.zst.part` files older than 7 days, and keeps the `-cli-zst` blob after a failure before the new CLI is in place. On Windows it uses the default CLI folder and `<version>.exe`. `-cli-probe-timeout` is ignored.
