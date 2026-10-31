---
default: minor
---

files.list on Windows counts a link as a folder only if the link has the folder attribute, a junction only with junctionsAsDirs, and gives false for a share or a chain over 31 links.
