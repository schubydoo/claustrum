---
default: minor
---

The new `-check-place` flag checks the place that `CLAUDE_SSH_RUN_FROM` names, as 13047a51 does. Every mode but `-version` and a lone `-stop` checks it first. A refused place exits 78 with one stderr line. `-probe-cli` answers `__CLI_BAD__` instead, and a refused `-serve` removes its `-token-file` unless it is a folder.
