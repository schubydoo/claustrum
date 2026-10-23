---
default: minor
---

`files.extract_tar` now gives a file with the owner's execute bit mode `0700` on Linux and macOS, also refuses the home folder by another spelling such as a link, refuses a name that leaves `destDir` and comes back, and uses the reference error texts. `server.capabilities` lists `files.extract_tar.execBit`.
