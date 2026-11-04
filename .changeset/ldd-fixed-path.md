---
default: minor
---

On Linux the libc probe of -install runs /usr/bin/ldd with only LC_ALL=C and PATH=/usr/bin:/bin in its environment, never an ldd from the PATH of the operator.
