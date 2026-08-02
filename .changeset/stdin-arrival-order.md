---
default: patch
---

Pipelined `process.stdin` requests on one connection now reach the child in the order they were sent, instead of racing each other and delivering the byte stream scrambled.
