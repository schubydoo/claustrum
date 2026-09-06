---
default: patch
---

git.status now passes `--attr-source=<empty-tree>` (when the runtime git supports it, added in 2.40) so a repository's in-repo `.gitattributes` cannot influence the status output or run its filter commands during `git status`, matching reference build 4534d86.
