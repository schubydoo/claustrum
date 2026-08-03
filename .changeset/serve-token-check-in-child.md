---
default: patch
---

`claustrum -serve` with neither `-token-file` nor `-token-fd` now daemonizes and refuses to start in the detached child, so the launcher reports its ~10s accept timeout exactly as the reference does, instead of exiting immediately with the specific reason.
