---
default: patch
---

On Linux and macOS a child whose start returns after the shutdown began gets no signal and runs on after the daemon exits.
