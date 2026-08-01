---
default: patch
---

A `~`-prefixed path is now cleaned lexically, so `~/link/../x` means `~/x` even when `link` is a symlink, instead of letting the OS walk the symlink and read a different file.
