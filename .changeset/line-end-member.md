---
default: minor
---

Every reply line and stream frame line now ends with an `end` member that holds the byte count of the line before it (`len`) and the bytes sent on the connection before it (`at`), as the reference does.
