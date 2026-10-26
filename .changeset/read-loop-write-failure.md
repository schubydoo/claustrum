---
default: patch
---

The daemon now closes a connection when the write of its `-32001` or `-32700` reply fails, as `c8e2f083` does. A failed write of any other reply keeps the connection.
