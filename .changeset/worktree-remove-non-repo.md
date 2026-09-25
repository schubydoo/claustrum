---
default: patch
---

Without `worktreeRoot`, `git.worktree_remove` now matches reference build `f6010b97` for a `baseRepo` in which git finds no repository, and `90fca6e6` answers the same. If `baseRepo` is such an existing directory, the daemon no longer deletes `worktreePath`. Examples are a plain directory, a repository whose `.git` lacks `objects/`, and a daemon `GIT_DIR` that names nothing usable. Before, git's own remove failed there, and the daemon then deleted `worktreePath` and answered success. It now answers `{"success":false}` with the error `failed to remove worktree: could not check whether <p> is locked (its registrations could not be examined); retry`, and nothing is deleted. Without `worktreeRoot`, a `baseRepo` that the daemon can open but not search now answers `failed to remove worktree: statat .claude: permission denied`, and one that it cannot open at all (mode 0000) answers `failed to remove worktree: open <baseRepo>: permission denied`, as `f6010b97` does.
