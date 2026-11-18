---
default: patch
---

On macOS a new `.git/worktrees` folder of a shared repository now gets its shared mode without the set-group-ID bit when `.git` has a group that the user is not in.
