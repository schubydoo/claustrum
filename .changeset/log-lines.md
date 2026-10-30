---
default: patch
---

The daemon log gets connection lines of its own. A connection that never shows the token logs no New connection line and a counted close line. Refusals and parse errors log by count. The address and the write and scan error texts are quoted. The exit line names this daemon as the one that asked the child to end.
