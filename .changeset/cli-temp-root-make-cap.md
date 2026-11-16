---
default: patch
---

A process.spawn with cliTempRoot makes at most 8 new private temp folders in a minute, and the next spawn that needs one gets the text of the cap and no folder.
