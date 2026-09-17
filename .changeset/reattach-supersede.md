---
default: minor
---

`process.reattach` now closes the connection it replaces, matching reference build `90fca6e6`. A reattach already transferred the frame stream, so the previously attached connection stopped receiving frames. It stayed open, though, and gave a client no sign that the session had moved. The daemon now logs `[Server] closing connection <addr>: process <id> reattached from another connection` and closes it once. The idle watcher shares the same deliberate-close flag, so a superseded connection is never closed or logged a second time.
