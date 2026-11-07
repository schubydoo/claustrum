---
default: patch
---

`-bridge` sends its `server.adoptEnv` frame on at most two connections and then relays stdin. It sends a frame of up to 262144 bytes, and on Linux and macOS it names a value that is not UTF-8 in `kept`.
