---
default: minor
---

On Linux and macOS, after the repository check exits 128 and its second run exits 0, the daemon runs the check a third time with no config of the user or the system. If that run works, the reply is `git could not check this repository on the remote machine` with the text of the first check and a hint to fix that setting. The three runs share the 30 second limit of the first one, and a bare repository gets no third run. A git directory that fails the git-directory test right after a good check now gets the "no repository" answer.
