---
default: patch
---

`process.reattach` now transfers the frame stream to the reattaching connection like the reference, so a previously attached connection stops receiving instead of both getting every frame after a resume.
