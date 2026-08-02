---
default: patch
---

A `~`-prefixed path is now cleaned lexically like the reference — `~/link/../x` means `~/x` even when `link` is a symlink, `~//f` and `~/a/./b` and a trailing `~/wt/` collapse, and bare `~` alone stays verbatim — which is wire-visible on eight frames, most sharply on `files.stat`/`files.validate` where a trailing separator on a regular file previously answered `not a directory` and now succeeds.
