---
default: patch
---

In the state `shadow`, `git.worktree_create` now refuses more than 64 filters in a repository config. So does the last config listing before the checkout when it fails or runs 30 seconds. With a user `lfs.storage` the checkout gets no `lfs.storage` item of the daemon. A failing user `smudge` fails the request where the repository adds only `required = true`, `yes` or `1`.
