---
default: patch
---

The daemon now writes the parse error (-32700) and the auth error (-32001) on its read path, before it reads the next line. A client that half-closes right after a bad line now gets that reply, with or without a final newline. Stdin EOF no longer ends the `-bridge` mode, which keeps relaying the daemon's replies. Measured against reference builds `f6010b97` and `90fca6e6` on a Linux VM, the reference did the same.
