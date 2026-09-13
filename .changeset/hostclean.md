---
default: minor
---

At startup the daemon now runs a periodic host cleaner. It ends stranded sibling daemons and orphaned Claude Code process groups under this install and tidies their stale run dirs. This reproduces reference build `19f30c46`'s host cleaner (off-wire), with a few documented, conservative simplifications.
