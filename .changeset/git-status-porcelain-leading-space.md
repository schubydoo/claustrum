---
default: patch
---

Keep the leading space on `git.status` porcelain lines

`git.status` ran each `git status --porcelain` line through
`strings.TrimSpace`, which ate the leading space of every unstaged-only change.
The two-character XY column is positional, so a staged modify (`"M  f"`) and an
unstaged one (`" M f"`) arrived differing only in space count, and a client
could no longer tell them apart — the corruption hit the most common case.
Lines now pass through verbatim apart from the line ending
(`strings.TrimRight(line, "\r\n")`); blank lines are still skipped.

The golden fixture missed this for months because it contained only untracked
files (`?? f`), the one shape with no leading space. A new socket test pins all
seven XY shapes: staged, unstaged, both, staged-delete, unstaged-delete,
rename, and untracked.
