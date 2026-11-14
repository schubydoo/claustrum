---
default: minor
---

On Linux and macOS a failed LFS checkout of `git.worktree_create` gets a new note for a full disk, a full quota or a missing object. The checkout copies no local object that would leave less than 1 GiB free, and the failure then names the space. On Windows the ssh note now also follows a text whose cut drops `smudge filter lfs failed`.
