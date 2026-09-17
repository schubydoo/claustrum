---
default: minor
---

`git.worktree_create` now seeds a new worktree the way the reference does. It copies the git-ignored files under `.claude/`, except `.claude/worktrees/`. It passes `-z`, so a filename holding a tab, a quote, a backslash or a non-ASCII byte is copied instead of silently dropped. It also skips nine Claude runtime-state paths such as `scheduled_tasks.json` and `mailbox`, matching reference build `90fca6e6`. The first two were gaps against every reference build back to `5db5e4a`, measured on a linux VM against `19f30c46` and `90fca6e6` alike.
