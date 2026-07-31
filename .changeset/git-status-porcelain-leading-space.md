---
default: patch
---

Keep the leading space on `git.status` porcelain lines

`git.status` removed the leading space from each unstaged change, which hid the
difference between a staged change and an unstaged change. The lines now pass
through unchanged.
