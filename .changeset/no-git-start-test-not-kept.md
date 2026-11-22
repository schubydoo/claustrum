---
default: patch
---

A failed start test where no git can start is no longer kept, so the next git request tests again and runs a git that it can now use, as the reference does on Linux and Windows.
