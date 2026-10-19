---
default: patch
---

With a settings copy `git.worktree_create` now follows the filter rule of `5fd08069` in four states. A repository `process` key beside a user filter no longer fails the request, and a user `process = cat` no longer counts as a definition outside the repository. A repository-only filter whose last `smudge` or `process` line has no `=` is now refused, and on Linux a repository filter whose name is not valid UTF-8 and whose value does not pass gets git's checkout text. With no settings copy such a name now gets the text about values that no option of git can replace.
