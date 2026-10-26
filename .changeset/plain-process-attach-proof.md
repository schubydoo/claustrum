---
default: minor
---

`process.stdin` with `attach` answers `Process not found`, and `process.reattach` with a non-empty `proof` answers the zero result, as `c8e2f083` does for a process without a terminal.
