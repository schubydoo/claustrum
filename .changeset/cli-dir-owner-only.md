---
default: patch
---

`-install` now creates the CLI directory chain owner-only (`0700`) like the reference, instead of `0755` which left the installed CLI world-traversable.
