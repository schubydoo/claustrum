---
default: patch
---

A failed start test where no git can start is no longer kept, so the next git request tests again and runs a git that it can now use, as 87375cc7 does on Linux and Windows.
