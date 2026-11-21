---
default: patch
---

process.spawn now tests the inherited temp folders one after another, with up to 2 s for each, so two folders that do not answer delay the reply about 4 s, as the reference does.
