---
default: patch
---

`git.list_branches` reads git's stdout only and reports a failed `for-each-ref` as `-32603 exit status <n>`, so a repository with a broken ref no longer lists git's `warning:` line as a branch and one with a corrupt refs database no longer returns that failure as branch names.
