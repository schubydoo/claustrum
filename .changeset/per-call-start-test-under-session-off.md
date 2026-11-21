---
default: patch
---

Without shadowDirOff, each git call now repeats the start test while session folders are off. This holds until a state is kept, and each call reads the excludes again too, as the reference does on Linux.
