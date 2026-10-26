---
default: patch
---

Before each uncached call about a repository, the daemon now cleans its own `.probe` folder. It removes leftover git entries and replaces a folder at `HEAD`. A `.probe` folder that cannot be prepared now refuses the request with no git call. The reference does both on Linux for the entries and the refusal that were measured.
