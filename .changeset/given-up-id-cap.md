---
default: patch
---

`process.spawn` keeps at most 4096 given-up ids: a full set first drops its expired ids, and a give-up that finds no room is dropped.
