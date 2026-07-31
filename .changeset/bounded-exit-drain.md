---
default: patch
---

The `exit` stream frame now arrives at most 5 seconds after a spawned process exits, instead of waiting forever when the command left a background grandchild holding its stdout.
