---
default: patch
---

In the state shadow, git.worktree_remove asks git for the git directory again when the answer names another repository. A second such answer refuses the remove with the disagree text, also with worktreeRoot, and nothing is deleted.
