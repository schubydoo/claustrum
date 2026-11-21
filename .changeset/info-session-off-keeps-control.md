---
default: patch
---

On macOS and Windows, a git.info with "sessionFoldersOff":true after a request without it now keeps the settingsControl member "shadow", as the reference does.
