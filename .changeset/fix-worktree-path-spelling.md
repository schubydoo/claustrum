---
default: patch
---

On Windows, refuse a `git.worktree_create`/`worktree_remove` path whose component ends in a dot or space or contains a colon — Windows reads it as a different name — with the reference's spelling refusal, instead of letting it fall through to a later, inconsistent failure.
