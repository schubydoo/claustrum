---
default: patch
---

git.status now keeps the leading space on the first change line (for example ` M a.txt`), passing the porcelain through verbatim to match reference builds 7d193f89 and 4534d86, instead of the older 5db5e4a behaviour that trimmed the first line's leading space.
