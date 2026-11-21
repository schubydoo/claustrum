---
default: patch
---

On Windows git.status now drops the name before a .. first, also a missing name, as the reference does, so such a baseRepo is judged as its cleaned path.
