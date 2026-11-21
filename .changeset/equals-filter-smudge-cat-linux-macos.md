---
default: patch
---

On Linux and macOS git.worktree_create now passes a filter that only the repository defines, with an equals sign in its name and cat as its smudge. This holds with a settings copy, as 4c111e86 does.
