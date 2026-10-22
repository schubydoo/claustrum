---
default: patch
---

On Linux the `-serve` launcher now exits 1 with `claustrum: locate self: <error>` before it makes any file when it cannot locate its own binary, as `5fd08069` does (round `tier3-02`, Linux VM).
