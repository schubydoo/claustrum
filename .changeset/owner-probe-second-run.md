---
default: patch
---

After git exits 128 on the repository check, the daemon runs the check once more with `GIT_DIR` set to its `.probe` folder, as the reference does. If that run exits 0, the method gives its "no repository" answer. If it fails or runs past 30 seconds, the reply is `git could not check this repository on the remote machine` with the text of the first check. `git.info` now runs this check on a folder whose `.git` file names a gone path. A failed config listing that names a git directory, as in a folder with no repository, now gets the hooks refusal after a `git version` call. Without such a name, the daemon's own search for a git directory gives the "no repository" answer, in any language of git.
