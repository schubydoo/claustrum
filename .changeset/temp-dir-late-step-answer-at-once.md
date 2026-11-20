---
default: patch
---

On Linux and macOS, while the private temp folder step of a temp dir goes on after its 2 s limit, a spawn with that temp dir now gets the text of the limit at once instead of after another 2 s.
