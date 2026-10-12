# Repository config refusals: measurement record

This page is a record, not reading material. It holds the cells behind the refusals of a repository config on the five git methods. The rules for a reader are in [Repository config refusals](../PROTOCOL.md#repository-config-refusals).

Each cell ran the same request on `89cb6289` and on `5fd08069`, and on the claustrum of that day. In the first two rounds that claustrum answered as `89cb6289` on the Linux and macOS VMs. The third round is in "The validation round", the fourth in "The second validation round", and the fifth in "The third validation round". The Linux VM has git 2.43.0. A cell name is the name of its round, so one name can stand in two tables.

In the tables, "the config" is `<repo>/.git/config`. "Class 1" is a text that starts with `this repository's git config cannot be used: `. "Class 2" is a text that starts with `a file in this repository's .git directory cannot be used: `. On `git.info` and `git.status` that text is the message of a `-32603` error. Each table gives the detail after that prefix. "Normal" is the answer of a plain repository.

`git.status` reports for a linked worktree of `baseRepo`. So each `git.status` cell sends a linked worktree as `path`.

## The answer of each method

| Cell | System | Request | `5fd08069` |
|---|---|---|---|
| C2 | Linux VM, Windows VM | `git.info` and `git.status`, format version `2` | `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"this repository's git config cannot be used: the repository uses format version \"2\", which this version of Claude does not support"}}` |
| W7 | Linux VM | `git.worktree_create`, format version `2` | `{"success":false,"error":"this repository's git config cannot be used: the repository uses format version \"2\", which this version of Claude does not support","errorCode":"worktree_add_failed"}`. Nothing is made |
| W7 | Linux VM | `git.worktree_remove` of a worktree that exists, format version `2` | `{"success":false,"error":"failed to remove worktree: could not check whether <repo>/.claude/worktrees/wr is locked: this repository's git config cannot be used: the repository uses format version \"2\", which this version of Claude does not support"}`. The worktree stays |
| W7 | Linux VM | `git.worktree_create`, a config of 20 MiB | The same shape with `\"<repo>/.git/config\" is larger than 8 MiB`, in 0.17 s |
| W7 | Linux VM | `git.worktree_remove`, a config of 20 MiB | The same shape as the remove above with that detail. No git call runs, and the worktree stays |

In cell W7 `89cb6289` answers `not_a_repo` for the create with format version `2`. For that remove it answers the lock-check refusal with `(its registrations could not be examined); retry`. With the config of 20 MiB it creates the worktree after 7.2 s and removes the other one.

Cell X1 of the edge round ran `git.list_branches` with a refused config. Cells F and G of the validation round ran a remove of a worktree folder that does not exist.

## The read

The call log of `5fd08069` shows one call for each config file: `git config --no-includes --file - --list -z`.

| Cell | System | Fact |
|---|---|---|
| C2a | Linux VM | The call runs in the root directory. Its environment has `GIT_DIR=/dev/null`, `LC_ALL=C` and `LANGUAGE=C` |
| C2a | Windows VM | The call has `GIT_DIR=NUL`, `LC_ALL=C` and `LANGUAGE=C` |
| C2a | Linux VM | A refused `git.status` makes this one git call |
| E10 | Linux VM | With 17 included files in a row, a refused `git.status` makes 17 of these calls |
| E10 | Linux VM | With a chain of depth 4, a refused `git.status` makes 4 git calls |
| E10 | macOS VM | With an `includeIf "gitdir:<repo>/"` section that matches, the config that the daemon writes for git has no setting of the included file |
| A7b | Linux VM | With `extensions.worktreeConfig` and a `config.worktree` file, that config has no setting of the `config.worktree` file |
| B4 | Linux VM | A second `git.info` with no change makes 8 git calls. A third one, after a change of the config, makes 9 |
| B3 b | Linux VM | `git.info` and `git.status` on a linked worktree answer normally |

## Format version

Cells E8 ran on Linux, macOS and Windows VMs, with `git.info` and `git.status`. The detail is `the repository uses format version "<v>", which this version of Claude does not support`.

| `core.repositoryformatversion` | `89cb6289` | `5fd08069` |
|---|---|---|
| `1` | normal | normal |
| `" 0"`, written with quotes | normal | class 1 with `" 0"` |
| `00` | normal | class 1 with `"00"` |
| `-1` | normal | class 1 with `"-1"` |
| `1.0` | the hooks refusal with `bad numeric config value '1.0'` | class 1 with `"1.0"` |
| the empty value | the hooks refusal with `bad numeric config value ''` | class 1 with `""` |
| `0`, then `2` | `isRepo` false | class 1 with `"2"` |
| `2`, then `0` | normal | normal |
| `2` (cell C2, Linux and Windows VMs) | `isRepo` false | class 1 with `"2"` |

## Extensions

Cells E9 ran on Linux, macOS and Windows VMs with format version `1`. The detail is `the repository uses git extension "<name>", or a value of it, that this version of Claude does not support`.

| Setting | `89cb6289` | `5fd08069` |
|---|---|---|
| `noop = true` | normal | normal |
| `preciousObjects = true` | normal | normal |
| `partialClone = origin` | normal | normal |
| `refStorage = files` | normal on macOS and Windows VMs. `isRepo` false on the Linux VM, where git 2.43.0 refuses the extension | the same as `89cb6289` on each system |
| `refStorage = banana` | `isRepo` false on the Linux VM. The hooks refusal on macOS and Windows VMs | class 1 with `"refstorage"` |
| `objectFormat = md5` | the hooks refusal | class 1 with `"objectformat"` |
| `foo = false` | `isRepo` false | class 1 with `"foo"` |
| `foo` with no value | `isRepo` false | class 1 with `"foo"` |
| `foo` (cell C2, Linux and Windows VMs) | `isRepo` false | class 1 with `"foo"` |

With format version `0`:

| Cell | System | Setting | `89cb6289` | `5fd08069` |
|---|---|---|---|---|
| C2 | Linux VM, Windows VM | `extensions.foo` | normal | normal |
| C2, A3 | Linux VM, macOS VM, Windows VM | `extensions.objectformat = md5` | the hooks refusal | normal |
| C2 | Linux VM, Windows VM | `extensions.worktreeConfig` with a `config.worktree` file | normal | normal |
| A3 | Linux VM, macOS VM | `extensions.objectformat = sha256` in a sha1 repository | `isRepo` false | `isRepo` false |

## Includes

| Cell | System | `include.path` | `89cb6289` | `5fd08069` |
|---|---|---|---|---|
| C3 | Linux VM | a relative path inside `.git`, an absolute path, `~/x` with a home folder, a missing file | normal | normal |
| C3 | Linux VM | `includeIf` | normal | normal |
| C3 | Linux VM | `~root/x` | the hooks refusal with `unable to access '/root/x': Permission denied` | class 1: `"<repo>/.git/config" includes "~root/x", and Claude expands neither ~user nor %(prefix), nor ~ where no home directory is set; write that path out in full` |
| C3 | Linux VM | `%(prefix)/x` | normal | the same text with `"%(prefix)/x"` |
| C3 | Linux VM | a chain of 20 files, and a loop of two files | the hooks refusal with `exceeded maximum include depth (10)` | class 1: `its includes go past what Claude follows (16 files, nested 3 deep)` |
| E10 | Linux VM, macOS VM | 15 and 16 files in a row | normal | normal |
| E10 | Linux VM, macOS VM | 17 files in a row | normal | the "includes go past" text |
| E10 | Linux VM, macOS VM | a chain of depth 2 and of depth 3 | normal | normal |
| E10 | Linux VM, macOS VM | a chain of depth 4 | normal | the "includes go past" text |
| E10 | Linux VM, macOS VM | `includeIf "gitdir:<repo>/"` that matches, and one that does not match and names a missing file | normal | normal |
| C3a | Windows VM | a relative path inside `.git` | `git.status`: `exit status 128` | `git.status`: `exit status 128` |
| C3b | Windows VM | `../../outinc.cfg`, a file above the repository | `git.status`: `exit status 128` | class 1: `"<included file>" is outside "<repo>", and on Windows Claude reads a repository's settings only from inside it; if the line that includes it is yours and you trust the file, put that line into ~/.gitconfig on the remote machine instead` |
| C3c, C3d | Windows VM | an absolute path, an absolute path of a missing file, and `~/x` | `git.status`: `exit status 128` | class 1: `"<repo>\\.git\\config" includes "<value>", and on Windows Claude follows an include only if its path is relative and stays inside the repository; if that line is yours and you trust the file, put the line into ~/.gitconfig on the remote machine instead` |

In a Windows row each backslash of a path is doubled inside the quotes, as in the message. A chain of depth N is the config, then `d1.cfg` to `dN.cfg`, each included by the file before it. On the Windows VM a plain repository with no user excludes file gets the same `exit status 128` from `git.status`, on both builds. So that answer is not from the include.

## File kind and size

| Cell | System | State | `89cb6289` | `5fd08069` |
|---|---|---|---|---|
| C4 | Linux VM | the config is a FIFO | no answer in 10 s | class 1: `"<repo>/.git/config" is not a regular file`, in 160 to 240 ms |
| C4 | Linux VM | the config is a symbolic link to a file | normal | normal |
| C4 | Linux VM | the config is a symbolic link to `/dev/null` | normal | the "not a regular file" text |
| C4 | Linux VM | the config is a directory | the hooks refusal with `Is a directory` | the "not a regular file" text |
| C4 | Linux VM | a config of 20 MiB | normal after 3.8 s and more | class 1: `"<repo>/.git/config" is larger than 8 MiB` |
| C4a | Windows VM | a config of 20 MiB | `git.info` normal after 10 s | the same text, in 0.6 s |
| C4 | Linux VM | one value of 5000 bytes | normal | normal |
| C4b | Windows VM | the config is a file link | `git.info` normal | class 1: `"<repo>\\.git\\config": is, or is reached through, a link that Claude does not follow here; replace the link with what it points to` |
| E10 | Linux VM, macOS VM | an included file is a FIFO | no answer in 10 s | class 1: `"<repo>/.git/fifo.cfg" is not a regular file`, in 327 ms and 13 ms on the Linux VM |
| E10 | Linux VM, macOS VM | an included file is a directory | the hooks refusal with `Is a directory` | the "not a regular file" text with the path of that file |
| E4 | Linux VM | another process replaces the config 50 times per second | 20 of 20 normal | 20 of 20 normal |

## Key names and counts

| Cell | System | Config | `89cb6289` | `5fd08069` |
|---|---|---|---|---|
| E2 | Linux VM, macOS VM, Windows VM | a setting name of 300 bytes, in `[core]` and in `[remote "o"]` | normal | normal |
| E2 | Linux VM, macOS VM, Windows VM | a setting name of 1100, 5000 and 70000 bytes in `[core]` | normal | class 1: `"<repo>/.git/config" has a setting in section "core" whose name is longer than 1024 bytes` |
| E2 | Linux VM, macOS VM, Windows VM | the same in `[remote "o"]` | normal | the same text with `section "remote"` |
| E3 | Linux VM | a subsection name of 70000 bytes in `[remote "…"]` | normal | the same text with `section "remote"` |
| E1 | Linux VM, Windows VM | `core.filemode = true` 200000 times, a file of 3.4 MB | normal | class 1: `"<repo>/.git/config" has more settings than Claude reads (8 MiB); remove repeated ones`. `git.worktree_create` answers it with `worktree_add_failed` |
| E1 | Linux VM, Windows VM | 200000 different keys, a file of 3.4 MB | normal | normal |

The bounds of the last text are in the edge round below.

## Files of the git directory

These cells ran on the Linux VM, with `git.info` and `git.status`.

| Cell | State | `89cb6289` | `5fd08069` |
|---|---|---|---|
| C5 | `info/attributes`, `info/exclude` or `shallow` is a symbolic link | normal | normal |
| C5 | one of the three is a FIFO | no answer in 10 s | class 2: `"<repo>/.git/<file>" is not a regular file` |
| E6 | an `info/exclude` of 20 MiB | normal | class 2: `"<repo>/.git/info/exclude": it is larger than 4 MiB` |
| E6 | an `info/attributes` of 20 MiB | normal | the same text with `info/attributes` |
| E7 | `shallow` holds the line `this line is not an object id` | `git.info` normal, `git.status` `exit status 128` | class 2: `"<repo>/.git/shallow" holds something other than object ids` |
| B3 d | a shallow clone, whose `shallow` holds one object id | normal | normal |

## No refusal

| Cell | System | State | `89cb6289` | `5fd08069` |
|---|---|---|---|---|
| E5 | Linux VM | `HEAD` holds a text that is no ref and no object id | `isRepo` false | `isRepo` false |
| C5 | Linux VM | `HEAD` is a symbolic link, or a FIFO | `git.status` answers `{"isRepo":false,"clean":false}` | the same |
| D1 | macOS VM, Windows VM | another user owns the repository, and no `safe.directory` is set | `isRepo` false, `not_a_repo` for the create, and the lock-check refusal for the remove | the same |

## The edge round

A later round ran the X cells on the Linux VM, with git 2.43.0, on the same three daemons. The claustrum of that day answered as `89cb6289` in each reply line of that round. Each bound below is a pair: the last accepted cell and the first refused cell.

### The methods, the switch and the order

| Cell | System | State | `89cb6289` | `5fd08069` |
|---|---|---|---|---|
| X1 | Linux VM | `git.list_branches` with `path`, on three refused fixtures | `isRepo` false for format version `2`, and normal for the two others | the error object of `git.info`, with the same text |
| X1 | Linux VM | format version `2`, and `"shadowDirOff":true` in the request | `isRepo` false | `git.info` and `git.status` answer `isRepo` false |
| X1 | Linux VM | a config of 20 MiB, or an `info/exclude` of 20 MiB, with `"shadowDirOff":true` | normal | normal |
| X1 | Linux VM | the config is a FIFO, with `"shadowDirOff":true` | no answer in 10 s | no answer in 10 s |
| X3 | Linux VM | the owner of the repository is another user, no `safe.directory` is set, format version `2` | `isRepo` false on three methods, `not_a_repo` for the create, the lock-check refusal with `; retry` for the remove | the same five answers. No version refusal |
| X3 | Linux VM | `HEAD` is no ref, format version `2` | the same five answers | the same five answers. No version refusal |
| X2 | Linux VM | `git.info` with a linked worktree as `path`. The config has format version `2`, or 20 MiB, or is a FIFO | `isRepo` false, normal, and no answer | class 1 with the text of that config |

The three fixtures of the first X1 row are format version `2`, a config of 20 MiB and an `info/exclude` of 20 MiB. A fourth fixture has a FIFO as the config. There `git.list_branches` came after two requests that gave no answer, and it gave none.

In the second X1 row, `git.info` and `git.status` got the member. Cells V2 of the validation round ran the three other methods.

Cell B3 of the first Linux round sent the member as `"true"`, `1` and `null`. `git.info` of `5fd08069` then has `"settingsControl":"shadow"`, as with no member. Cells V3 of the validation round ran such values with a refused config.

In the first X3 row plain git 2.43.0 answers `fatal: detected dubious ownership in repository at '<repo>'`.

### Format version and extensions

Cells X7: the value is the text that git lists.

| `core.repositoryformatversion` | `89cb6289` | `5fd08069` |
|---|---|---|
| `"1"` in quotes, `=    1` with spaces, `REPOSITORYFORMATVERSION = 1` | normal | normal |
| `01`, `+1`, `0x1` | normal | class 1 with `"01"`, `"+1"`, `"0x1"` |
| `1k` | `isRepo` false | class 1 with `"1k"` |
| `true` | the hooks refusal with `bad numeric config value 'true'` | class 1 with `"true"` |
| the key with no value | the hooks refusal with `bad numeric config value ''` | class 1 with `""` |
| the key is absent | normal | normal |
| `REPOSITORYFORMATVERSION = 2`, and `[CORE]` with that key | `isRepo` false | class 1 with `"2"` |
| `0` in the config, and `2` in an included file | normal | normal |

Cells X8 and X9, with format version `1`. Git 2.43.0 does not know some of these extensions, so both builds answer `isRepo` false there. No row of this table has a class 1 answer of `5fd08069`, except the last two.

| Setting | `89cb6289` | `5fd08069` |
|---|---|---|
| `objectFormat = sha1`, `worktreeConfig = true`, `worktreeConfig = false`, `noop-v1 = true`, `noop = 2` | normal | normal |
| `objectFormat = sha256` in a sha1 repository | `git.info` with no `branch` member, `git.status` `exit status 128` | the same |
| `compatObjectFormat = sha256`, `refStorage = reftable`, `relativeWorktrees = true`, `submodulePathConfig = true` | `isRepo` false | `isRepo` false |
| `partialClone` with no value | the hooks refusal with `signal: segmentation fault (core dumped)` | class 1 with `"partialclone"` |
| `preciousObjects = maybe` | the hooks refusal with `bad boolean config value 'maybe'` | class 1 with `"preciousobjects"` |

With format version `0`, `partialClone` with no value and `preciousObjects = maybe` are normal on `5fd08069`. An extension name in upper case gives the same answer as in mixed case. An `extensions.foo = true` in an included file, with format version `1` in the config, is normal on both builds (cell X9).

### The name bound

Cells X4 and Y4. `89cb6289` answers normally in each cell.

| Form | Last accepted | First refused | Section in the text |
|---|---|---|---|
| a key in `[core]` | a key of 1019 bytes | a key of 1020 bytes | `core` |
| a key in `[remote "o"]` | 1015 bytes | 1016 bytes | `remote` |
| a key in `[remote "<100 bytes>"]` | 916 bytes | 917 bytes | `remote` |
| the subsection of `[remote "<n>"]` with the key `url` | 1013 bytes | 1014 bytes | `remote` |
| a key in `[abcde]` | 2200 bytes, the largest that ran | none | no refusal |
| a section name with the key `key` | 2200 bytes, the largest that ran | none | no refusal |

In each refused form the full name `section.subsection.key` has 1024 bytes at the last accepted cell and 1025 at the first refused one.

A key of 1100 bytes, by section:

- Refused: `core`, `extensions`, `include`, `lfs`, `url "u"`, `filter "f"`, `includeIf "gitdir:/x/"`, `lfs "l"`, `core "o"`.
- The text names `url`, `filter`, `includeif`, `lfs` and `core` for the last five.
- Accepted: `user`, `alias`, `http`, `credential`, `safe`, `gc`, `diff`, `merge`, `x`, `branch "b"`, `submodule "s"`, `x "o"`.

### The count of settings

Cells X5 and Y5. N is the count of lines after the four lines of a `git init` config. `89cb6289` answers normally at each step, except in the second row: there git refuses the value and the answer is `isRepo` false.

| Lines | Last accepted N | First refused N |
|---|---|---|
| `core.filemode = true` | 74231 | 74232 |
| `core.filemode = <100 bytes>` | 40134 | 40135 |
| `core.filemode = true` and `core.bare = false` in turn | 75229 | 75230 |
| `remote.origin.url = abcd` | 71693 | 71694 |
| `core.bare = false` | 76255 | 76256 |
| keys of 5 bytes in `[core]`, no two equal, with the value `1` | 78393 | 78394 |
| `core.filemode =` with an empty value | 76955 | 76956 |
| `core.filemode` with no value | 76955 | 76956 |
| `x.y = true` | 838850, the largest file under 8 MiB | none |

With 200000 lines, `extensions.noop = true`, `include.path = nosuchfile.cfg` and `branch.main.remote = abcd` are refused. `user.name = abcd` is accepted, and so are 1000000 keys of `[x]` with no two equal.

claustrum fits an arithmetic to the pairs: see "The count of settings" in [Repository config refusals](../PROTOCOL.md#repository-config-refusals). The pairs of rows 5 to 8 were predictions of that arithmetic, and each one held. The exact pairs are in `core` and `remote`. Each pair also fits a bound from 8388572 to 8388613. The test `TestRepoConfigSettingsCost` has each pair.

### File sizes

Cells X6. `89cb6289` answers normally in each cell.

| File | Last accepted | First refused | Detail |
|---|---|---|---|
| the config, of comment lines, and of keys in `[x]` | 8388608 bytes | 8388609 bytes | `"<repo>/.git/config" is larger than 8 MiB` |
| `info/exclude` | 4194304 bytes | 4194305 bytes | `"<repo>/.git/info/exclude": it is larger than 4 MiB` |
| `info/attributes` | 4194304 bytes | 4194305 bytes | the same form |
| `shallow`, of valid ids | 4194304 bytes | 4194305 bytes | `"<repo>/.git/shallow" is larger than 4 MiB` |

With a `shallow` of 5 MiB, `git.status` of `89cb6289` gives no answer in 60 s (cell X11).

### Includes and `config.worktree`

| Cell | System | State | `89cb6289` | `5fd08069` |
|---|---|---|---|---|
| X9 | Linux VM | `include.path` with an empty value, and with no value | the hooks refusal | normal |
| X9 | Linux VM | `path = ../../out.cfg`, a file outside `.git` | normal | normal |
| X9 | Linux VM | an absolute path of a symbolic link to a FIFO | no answer | class 1: `"<link>" is not a regular file` |
| X9 | Linux VM | `includeIf "gitdir:<repo>/"`, and its file is a FIFO | no answer | class 1: `"<repo>/.git/im.cfg" is not a regular file` |
| X9 | Linux VM | the same, and its file has 20 MiB | normal | class 1: `"<repo>/.git/im.cfg" is larger than 8 MiB` |
| X9 | Linux VM | the same, and its file sets format version `2` | normal | normal |
| X9 | Linux VM | `includeIf "onbranch:main"` or `includeIf "hasconfig:remote.*.url:*"`, and its file sets format version `2` | normal | normal |
| X9 | Linux VM | the config includes itself, or an included file includes itself | the hooks refusal with `exceeded maximum include depth (10)` | the "includes go past" text |
| X9 | Linux VM | 16 files in a row, and the last one includes one more | normal | the "includes go past" text |
| X2 | Linux VM | `extensions.worktreeConfig = true`, `git.info` with the linked worktree `w0` as `path`, and `worktrees/w0/config.worktree` has 20 MiB | normal | class 1: `"<repo>/.git/worktrees/w0/config.worktree" is larger than 8 MiB` |
| X2 | Linux VM | the same, and that file is a FIFO | no answer | class 1 with `is not a regular file` |
| X2 | Linux VM | the same, and that file sets format version `2` | normal | normal |
| X2 | Linux VM | `.git/config.worktree` of the main worktree sets format version `2` | normal | normal |

In the X2 rows `git.info` on the repository itself answers normally on `5fd08069`.

### Other file kinds and the `shallow` text

Cells X10 and X11, with `git.info` and `git.status`.

| File | State | `89cb6289` | `5fd08069` |
|---|---|---|---|
| the config | a link to a link to a regular file, a dangling link, empty, missing, a NUL byte in a value | normal | normal |
| the config | a socket | the hooks refusal with `No such device or address` | class 1: `open <repo>/.git/config: no such device or address` |
| the config | mode 000 | the hooks refusal with `Permission denied` | class 1: `open <repo>/.git/config: permission denied` |
| the config | a syntax error in line 6 | the hooks refusal with `fatal: bad config line 6 in file <repo>/.git/config` | class 1: `fatal: bad config line 6 in "<repo>/.git/config"` |
| `info/exclude`, `info/attributes` | a link to `/dev/null`, a dangling link, mode 000 | normal | normal |
| `info/exclude`, `info/attributes` | a directory | normal, or `exit status 128` for `git.status` with `info/exclude` | class 2: `"<file>" is not a regular file` |
| `shallow` | a link to `/dev/null`, a directory | normal | class 2: `"<repo>/.git/shallow" is not a regular file` |
| `shallow` | a dangling link | normal | normal |
| `shallow` | mode 000 | normal | class 2: `open <repo>/.git/shallow: permission denied` |
| `shallow` | empty, one id with no final newline, CRLF line ends, a line of 64 hex characters | normal | normal |
| `shallow` | an id in upper case | normal | class 2: `"<repo>/.git/shallow" holds something other than object ids` |
| `shallow` | a blank line after an id, or first | `git.status` `exit status 128` | the same class 2 text |

### A long path

Cells X12, with a config of 20 MiB and the size text. An ASCII path in quotes is whole at 300 bytes and is cut to its first 300 bytes at 301 bytes. It has no mark of the cut. A path of 359 bytes and 199 characters is whole. Cells V12 of the validation round ran a path of more than 300 characters with characters of two bytes. The remove text holds the whole `worktreePath` at 900 bytes.

### An old git

Another round ran `5fd08069` with older gits on the Linux VM. With git 2.11.4 and 2.12.5 it gives no refusal for format version `2`: it answers `isRepo` false. With git 2.13.7, 2.17.6 and 2.43.0 it refuses. No git between 2.12.5 and 2.13.7 ran, and no git before 2.11.4. Format version `2` ran on the older gits in that round, and cells V18 of the validation round ran three more states.

## The validation round

A third round ran the cells again, and the V cells for the first time, on three systems. The Linux cells ran on two VMs with git 2.43.0. The macOS VM has git 2.50.1, and the Windows VM git 2.55.0. The daemons were `89cb6289`, `5fd08069` and a claustrum with a first form of these refusals. This part gives the answers of `5fd08069`. One Linux VM ran 418 cells, and the other ran the cells V9 to V19. The macOS VM ran 233 cells, and the Windows VM 203.

### The bounds on three systems

In the first five rows `5fd08069` gives the same pair on the three VMs.

| Bound | Last accepted | First refused |
|---|---|---|
| the size of the config | 8388608 bytes | 8388609 bytes |
| the size of `info/exclude` | 4194304 bytes | 4194305 bytes |
| the size of `shallow` | 4194304 bytes | 4194305 bytes |
| a key in `[core]` | 1019 bytes, a full name of 1024 | 1020 bytes |
| a key in `[remote "o"]` | 1015 bytes, a full name of 1024 | 1016 bytes |
| lines of `core.filemode = true`, Linux and macOS VMs | 74231 | 74232 |
| the same lines, Windows VM | 74229 | 74230 |

On the Windows VM the config of `git init` has six keys, and the cells kept it. On the macOS VM the X and V cells used the config text of four lines of the Linux cells.

The cells X7, X8, X10, X11 and X12 ran on the macOS and Windows VMs too. Their answers equal those of the Linux VM, with these notes:

- X10, macOS VM: a socket as the config gets `open <repo>/.git/config: operation not supported on socket`.
- X10, Windows VM: the syntax error and the directory ran. No FIFO, socket or mode 000 cell ran there.
- X12, macOS VM: a path is cut at 300 characters.
- X12, Windows VM: a config path of 212 and of 250 characters is whole. At 300 and 301 characters git does not start: see cells X12 and V12a below.
- X8, Windows VM: the extension names in upper case did not run.
- X8, macOS and Windows VMs: git knows these extensions there. `refStorage = reftable` answers `isRepo` true with no `branch` member. On the Windows VM `compatObjectFormat = sha256` gets the hooks refusal. The class 1 rows equal the Linux rows.

### The owner, the switch and the path

| Cell | System | State | `5fd08069` |
|---|---|---|---|
| V1 | Linux VM, macOS VM | the owner of the repository is another user. Format version `2`, a config of 20 MiB, a FIFO as the config, an `info/exclude` of 20 MiB | the "not a repository" answers of the five methods in each state |
| V1 | Windows VM | the same owner state, with the three states that have no FIFO | the same five answers |
| V2 | Linux VM, macOS VM, Windows VM | `"shadowDirOff":true` on `git.list_branches`, `git.worktree_create` and `git.worktree_remove`, with format version `2` | `isRepo` false, `not_a_repo`, and the lock-check refusal with `; retry` |
| V2 | Linux VM, macOS VM, Windows VM | the same member, with a config of 20 MiB or an `info/exclude` of 20 MiB | normal: the worktree is made, and the other one is removed |
| V2 | Linux VM, macOS VM | the same member, with a FIFO as the config | no answer in 10 s on each of the three methods |
| V3 | Linux VM, macOS VM, Windows VM | format version `2`, and `shadowDirOff` as `"true"`, `1`, `null` or `false`, on `git.info` and `git.status` | the class 1 text of format version `2` |
| V3 | Linux VM, macOS VM, Windows VM | format version `2`, and `"SHADOWDIROFF":true` | `isRepo` false, as with `"shadowDirOff":true` |
| V11 | Linux VM, macOS VM | a config of 20 MiB, and a request path through a symbolic link | the text names the resolved path of the config. The remove text has `worktreePath` as sent before it |
| V11 | Windows VM | the same, through a junction | the text has the spelling of the junction |
| V11 | Windows VM | the same, with the path in another letter case | the text has the real spelling of the config path, and `worktreePath` as sent |
| V13 | Linux VM, macOS VM | a FIFO as the config, as the first request of a new daemon | `git.list_branches` and `git.worktree_create` answer the "not a regular file" text in under 220 ms |
| V15 | Linux VM, macOS VM, Windows VM | format version `2`, `git.status` with the repository as `path`, and in a repository with no `worktrees` folder | the class 1 text of format version `2`. With version `0` the answer is `{"isRepo":false,"clean":false}` |

In the V1 cells of the Linux and macOS VMs plain git answers `fatal: detected dubious ownership in repository at '<repo>'` in each state, also with the FIFO. On the Linux VM that took 0.01 s. On the Windows VM that text is recorded for format version `2`, and in cell N17w for a config of 20 MiB.

### Extensions, names and counts

Cells V6 ran on the three VMs with format version `1`. EXT is the extension text of class 1.

| Setting | `5fd08069` |
|---|---|
| `bar = true`, `noop2` with no value | EXT with `"bar"`, `"noop2"` |
| `[extensions "x"] noop = true` | EXT with `"x.noop"` |
| `relativeWorktrees = maybe`, `submodulePathConfig = maybe`, `noop-v1 = maybe` | EXT with the name in lower case |
| `compatObjectFormat = md5` | EXT with `"compatobjectformat"` |
| `partialClone =` with an empty value | EXT with `"partialclone"` |
| `worktreeConfig = maybe`, `preciousObjects = yes`, `preciousObjects = 1` | normal |
| `objectFormat = SHA256` | `git.info` with no `branch` member, `git.status` `exit status 128` |
| `refStorage = FILES` | `isRepo` false on the Linux VM, normal on the macOS and Windows VMs |
| `foo = true` in a config with no format version key | normal |

Cells V4 and V5 ran on the Linux VM.

| Cell | Config | `5fd08069` |
|---|---|---|
| V4 | a full name of 1024 bytes in `lfs`, `url "u"`, `filter "f"`, `extensions`, `include` and `includeIf "gitdir:/x/"` | normal |
| V4 | a full name of 1025 bytes in the same six | the name text of class 1, with the section in lower case |
| V4 | a key of 1100 bytes in `index`, `feature` and `checkout` | the name text of class 1 |
| V4 | a key of 1100 bytes in `branch "b"`, and a subsection of 1100 bytes in `branch` | normal |
| V5 | 200000 lines of `lfs.url`, `index.version`, `url.u.insteadof`, `filter.f.smudge`, `feature.manyfiles` or `checkout.workers` | the "more settings" text |
| V5 | 200000 `path = nosuch.cfg` lines in `[includeIf "gitdir:/x/"]` | the "more settings" text |

### Included files

Cells V7 have `include.path = inc.cfg` in the config. Cells V8 have the lines of the table.

| Cell | System | State | `5fd08069` |
|---|---|---|---|
| V7 | Linux VM, macOS VM, Windows VM | the included file has 8388608 bytes | normal |
| V7 | Linux VM, macOS VM, Windows VM | it has 8388609 bytes, or 20 MiB | class 1: `"<repo>/.git/inc.cfg" is larger than 8 MiB` |
| V7 | Linux VM, macOS VM, Windows VM | it has a key of 1100 bytes in `[core]` | the name text with the path of `inc.cfg` |
| V7 | Linux VM, macOS VM, Windows VM | it has 200000 `core.filemode = true` lines | the "more settings" text with the path of `inc.cfg` |
| V7 | Linux VM, macOS VM, Windows VM | it has a syntax error in line 1 | class 1: `fatal: bad config line 1 in "<repo>/.git/inc.cfg"` |
| V7 | Linux VM, macOS VM | it has mode 000 | class 1: `open <repo>/.git/inc.cfg: permission denied` |
| V7 | Linux VM | it is a socket | class 1: `open <repo>/.git/inc.cfg: no such device or address` |
| V8 | Linux VM | 16 `include.path` lines of missing files | normal |
| V8 | Linux VM, macOS VM, Windows VM | 17 such lines | the "includes go past" text |
| V8 | Linux VM, macOS VM, Windows VM | 16 files that exist, then 5 missing ones | the "includes go past" text |
| V8 | Linux VM | 5 missing files, then 16 that exist | the "includes go past" text |
| V8 | Linux VM, Windows VM | 15 files, then a matching `includeIf "gitdir:<repo>/"` with a file that exists | normal |
| V8 | Linux VM, macOS VM, Windows VM | 16 files, then that `includeIf` section | the "includes go past" text |
| V8 | Linux VM, macOS VM | `includeIf "gitdir:/nonexistent/zz/"`, and its file is a FIFO | class 1: `"<repo>/.git/nm.cfg" is not a regular file` |
| V8 | Linux VM, macOS VM, Windows VM | an `includeIf "gitdir:…"` that does not match, and its file has 20 MiB | class 1 with `is larger than 8 MiB` and the path of that file |
| V8 | Linux VM, macOS VM, Windows VM | `includeIf "gitdir/i:<repo in another letter case>/"`, `"onbranch:main"` or `"hasconfig:remote.*.url:*"`, and a file of 20 MiB | the same size text |
| V8 | Linux VM, macOS VM | a matching `includeIf "gitdir:<repo>/"` with `path = ~root/x` | the "expands neither" text with `"~root/x"` |
| V8 | Windows VM | the same, and a plain `include.path = ~root/x` | the Windows text for an include value that is not relative |

### Text details

| Cell | System | State | `5fd08069` |
|---|---|---|---|
| V12 | Linux VM | a config path of 300 characters, most of them of two bytes | the path is whole |
| V12 | Linux VM, macOS VM | a config path of 301 characters | the path is cut to 300 characters, and it ends `/.git/confi` |
| V12 | Linux VM | an include value of 100 characters, ASCII or of two bytes | the value is whole |
| V12 | Linux VM | an include value of 101 characters, ASCII or of two bytes | the value is cut to its first 100 characters |
| V12 | Linux VM, macOS VM | an include value of 400 characters | the value is cut to its first 100 characters |
| V12 | Linux VM, macOS VM | a file of mode 000 at a path of 400 bytes | `open <the whole path>: permission denied` |
| V12b | Windows VM | an include value `~root/` with 394 more characters | the value is cut to its first 300 characters |
| V12c | Windows VM | `path = <dir>/inc.cfg`, and that file has `path = ~root/x` | the file in the text is `<repo>\.git\<dir>/inc.cfg` |
| V12d | Windows VM | the same with a file path of 400 characters | the path is cut to 300 characters, and the `/` of the value stays |
| X12, V12a | Windows VM | a config path of 300 or 301 characters, and a config of 20 MiB | `git could not check this repository on the remote machine: at "<path>": fork/exec <git.exe path>: The directory name is invalid.` |

In the last row plain git answers `fatal: cannot change to '…': Filename too long`. The remove of `5fd08069` has that text after `is locked: `.

### Files of the git directory, and two refused files

| Cell | System | State | `5fd08069` |
|---|---|---|---|
| V9 | Linux VM, macOS VM, Windows VM | an `info/exclude` of 20 MiB, on `git.list_branches`, `git.worktree_create` and `git.worktree_remove` | class 2 in the frame of each method. Nothing is made, and the worktree stays |
| V9 | Linux VM, macOS VM, Windows VM | a `shallow` file with a text line, on the same three methods | the same three frames with `holds something other than object ids` |
| V10 | Linux VM, macOS VM, Windows VM | a `shallow` line of 39, 41 or 63 hex characters, and an id with a space after it | class 2 with `holds something other than object ids` |
| V10 | Linux VM, macOS VM, Windows VM | a `shallow` file of one `\n`, and one of one `\r\n` | the same class 2 text |
| V10 | Linux VM | `shallow` is a socket | class 2: `open <repo>/.git/shallow: no such device or address` |
| V14 | Linux VM, macOS VM, Windows VM | `extensions.worktreeConfig = true`, a linked worktree outside `.claude/worktrees`, and its `config.worktree` has 20 MiB | class 1 with the path of that file on `git.info`, `git.status`, `git.list_branches` and `git.worktree_create` |
| V14 | Linux VM, Windows VM | the same with `yes`, `1`, `TRUE` and the key with no value | the same four answers, and no worktree is made |
| V14 | macOS VM | the same four values, on `git.info` | the same class 1 text |
| W14 | Linux VM | `git.worktree_remove` with that worktree as `baseRepo` | the remove frame with that class 1 text |
| V16 | Linux VM, macOS VM, Windows VM | format version `2` and an `info/exclude` of 20 MiB | the class 1 text of the version |
| V16 | Windows VM | a config of 20 MiB, a `shallow` with a text line and an `info/exclude` of 20 MiB | the class 1 text of the config |
| V16 | Linux VM, macOS VM | `shallow` is a FIFO, and `info/attributes` has 20 MiB | class 2 with `shallow` |
| V16 | Linux VM | `shallow` is a FIFO, and `info/attributes` is a directory | class 2 with `shallow` |
| V16 | macOS VM | `shallow` is a FIFO, and `info/attributes` is a FIFO | class 2 with `shallow` |
| V16 | Windows VM | a `shallow` with a text line, and an `info/attributes` of 20 MiB | class 2 with `shallow` |
| V16 | macOS VM | an `info/exclude` of 20 MiB, and `info/attributes` is a FIFO | class 2 with `info/attributes` |

In the V14 rows `git.status` has that worktree as `path` and as `baseRepo`. With the main repository as `baseRepo` it answers `{"isRepo":false,"clean":false}` on each daemon, also with no large file.

### A remove of a worktree folder that does not exist

These cells ran on the Windows VM. The request has a `worktreePath` that does not exist and a `branchName` that exists.

| Cell | State | `89cb6289` | `5fd08069` |
|---|---|---|---|
| F1a, F2a, W6x | format version `2`, in a plain and in a bare repository | `{"success":true,"branchKept":true}` | the remove frame with the class 1 text of the version |
| F1b, F2b, G1a | a config of 20 MiB | `{"success":true}`, and the branch is deleted in cell G1a | the remove frame with the size text. In cell G1a the branch stays |
| G1b | an `info/exclude` of 20 MiB | `{"success":true}`, and the branch is deleted | the remove frame with the class 2 text. The branch stays |
| G1c | an absolute include | `{"success":true}`, and the branch is deleted | the remove frame with the Windows include text. The branch stays |
| W6 | a bare repository with a relative include that leaves it | `{"success":true}` | the remove frame with the "outside" text, which names the bare repository |
| G1z | no refused file | `{"success":true}`, and the branch is deleted | the same |

### The git calls, a pty and an old git

| Cell | System | Fact |
|---|---|---|
| V19 | Linux VM, macOS VM | A first `git.info` of `5fd08069` makes 21 git calls, a second one 8, and one after a change of the config 9. `89cb6289` makes 9, 8 and 8 |
| V19 | Windows VM | Those counts are 23, 10 and 11 on `5fd08069`. A `git.info` with format version `2` makes 1 git call there |
| V11b, V12a | macOS VM | After a refusal, a later request of `5fd08069` makes no git call. It answers in 10 to 70 ms with a config of 20 MiB |
| V17 | Linux VM | The config is a link to a pty slave. `5fd08069` answers `git.info` with the "not a regular file" text. After the pty master closes, `89cb6289` and `5fd08069` still answer `server.ping`, in 6 of 6 runs |
| V18 | Linux VM | Git 2.11.4 and git 2.12.5 ran. `5fd08069` gives no refusal there for format version `2`, a config of 20 MiB and an `info/exclude` of 20 MiB. With a FIFO as the config it gives no answer in 10 s |

In the V18 cells `git.info` of `5fd08069` has `"settingsControl":"fallback-git"`.

## The second validation round

A fourth round ran the N cells on the three VMs, on `89cb6289`, `5fd08069` and the claustrum of that day. The Linux VM has git 2.43.0, the macOS VM git 2.50.1 and the Windows VM git 2.55.0. This part gives the answers of `5fd08069`.

### A remove in a refused repository

In cells N1 the request has a `worktreePath` that does not exist and a `branchName` that exists. In cells N14 it has a `worktreeRoot`, and the worktree is below that root.

| Cell | System | State | `89cb6289` | `5fd08069` |
|---|---|---|---|---|
| N1 | Linux VM, macOS VM, Windows VM | format version `2` | `{"success":true,"branchKept":true}` | the remove frame with the class 1 text of the version. The branch stays |
| N1 | Linux VM, macOS VM, Windows VM | a config of 20 MiB | `{"success":true}`, and the branch is deleted | the remove frame with the size text. The branch stays |
| N1 | Linux VM, macOS VM, Windows VM | an `info/exclude` of 20 MiB | `{"success":true}`, and the branch is deleted | the remove frame with the class 2 text. The branch stays |
| N1 | Linux VM, Windows VM | the same three states in a bare repository | Windows VM: as in the three rows above | the same three frames, with the paths of the bare repository. The branch stays |
| N1 | Linux VM, macOS VM, Windows VM | no refused file, in a plain and in a bare repository | `{"success":true}`, and the branch is deleted | the same |
| N14 | Linux VM | `worktreeRoot`, format version `2`, for a worktree that exists and for a folder that is missing | `{"success":false,"error":"failed to remove worktree: cannot determine the repository's work tree: exit status 128"}` | `{"success":false,"error":"failed to remove worktree: cannot determine the repository's work tree: this repository's git config cannot be used: the repository uses format version \"2\", which this version of Claude does not support"}`. Nothing is removed |
| N14 | Linux VM | `worktreeRoot`, a config of 20 MiB, and the worktree exists | `{"success":true}`. The worktree folder and its registration are gone | the same frame as above with `\"<repo>/.git/config\" is larger than 8 MiB`. The folder and the registration stay |
| N14 | Linux VM | the same, and the folder is missing | `{"success":true}` | the same refusal frame |
| N14 | Linux VM | `worktreeRoot`, no refused file | `{"success":true}`. The folder and the registration are gone | the same |

### The owner, a FIFO and a terminal

| Cell | System | State | `5fd08069` |
|---|---|---|---|
| N2 | Linux VM, macOS VM | another user owns the repository, and the config is a FIFO | the "not a repository" answers of the five methods, in 13 to 240 ms |
| N2 | Linux VM, macOS VM | the same FIFO, and the daemon's own user owns the repository | the "not a regular file" text in the frame of each method. The first request takes 158 ms on the Linux VM and 361 ms on the macOS VM. The four later ones take under 2 ms |
| N3 | Linux VM, macOS VM | the config is a link to a pty slave | `git.info` answers the "not a regular file" text in 150 to 360 ms. After the pty master closes, `server.ping` answers and `git.info` is normal |
| N15-a | Linux VM | another user owns the repository, and the config has 20 MiB. Then `safe.directory = *` appears in the user config | `isRepo` false before the line. After it, the class 1 size text at once and 6 s later. The remove answers the remove frame with that text |
| N15-b | Linux VM | format version `2`. Then another user gets the repository | the class 1 text before the change and right after it. 6 s later `git.info` answers `isRepo` false |
| N16 | Linux VM | `HEAD` is a link to a pty slave | `isRepo` false on `git.info` and `git.status`, before and after the pty master closes |
| N16 | Linux VM | the `.git` file of a linked worktree is such a link | `git.info` on that worktree gives the answer of the main repository, before and after the close |
| N17 | Linux VM | a FIFO as the config, as the first request of a new daemon | `git.list_branches` answers in 155 ms and `git.worktree_create` in 255 ms. No git process is left after the request |
| N17w | Windows VM | another user owns the repository, and the config has 20 MiB. `git.info` five times | `isRepo` false five times. No git process is left after a request |

In cells N3 and N16 `89cb6289` and `5fd08069` stay up at each step. Cell N3 ran 6 times on the Linux VM and 5 times on the macOS VM, and cells N16 ran 5 times each. With the daemon's own user as the owner, `89cb6289` gives no answer in 10 s to the first request of cells N2 and N3. In cell N17 `89cb6289` leaves one `git config -z --list` process until the daemon stops.

### Values, names and counts

| Cell | System | State | `5fd08069` |
|---|---|---|---|
| N4 | Linux VM, Windows VM | `extensions.worktreeConfig = on` or `= ON`, a linked worktree outside `.claude/worktrees`, and its `config.worktree` has 20 MiB | class 1 with the path of that file on `git.info`, `git.status`, `git.list_branches` and `git.worktree_create`. No worktree is made |
| N4 | macOS VM | the same two values, on `git.info` | the same class 1 text |
| N4 | Linux VM, macOS VM, Windows VM | the same with `false`, `no` or `0` | normal |
| N5 | Linux VM, macOS VM, Windows VM | one included file with 40000 `core.filemode = true` lines | normal |
| N5 | Linux VM, macOS VM, Windows VM | two such included files | the "more settings" text with the path of the second file |
| N5 | Linux VM, macOS VM, Windows VM | 40000 such lines in the config, and one such included file | normal |
| N5 | Linux VM | the same, with the include line before the 40000 lines | normal |
| N6 | Linux VM, macOS VM, Windows VM | a full name of 1024 bytes in `index`, `feature` and `checkout` | normal |
| N6 | Linux VM, macOS VM, Windows VM | a full name of 1025 bytes in the same three | the name text of class 1 with `"index"`, `"feature"` or `"checkout"` |
| N11 | Linux VM, Windows VM | a format version of 100, 101, 300, 301 or 1100 characters `7` | the version text with the first 20 characters, and no mark of the cut |
| N11 | Linux VM, Windows VM | format version `1`, and an extension name of 100, 101, 300, 301 or 900 characters `z` | the extension text with the first 60 characters, and no mark of the cut |

### Included files

| Cell | System | State | `5fd08069` |
|---|---|---|---|
| N7 | Linux VM, macOS VM, Windows VM | 16 included files that exist, and one `includeIf "gitdir:…"` section whose file is missing | the "includes go past" text, with a condition that matches and with one that does not |
| N7 | Linux VM, macOS VM, Windows VM | 15 such files and two such sections | the same text, with both kinds of condition |
| N7 | Linux VM, macOS VM | 15 such files and one such section | normal |
| N8 | Linux VM, macOS VM, Windows VM | a key of 1100 bytes in `[core]`, in a file two levels below the config, and in one three levels below it | the name text of class 1 with the path of that file |
| N8 | Linux VM | the same key four levels below the config | the "includes go past" text |
| N12 | Linux VM | an `includeIf` section whose condition does not match, with `path = ~root/x` or `path = %(prefix)/x` | the "expands neither" text with that value |
| N12w | Windows VM | `includeIf "gitdir:C:/elsewhere/"` with an absolute path, or with `path = ~/x` | the Windows text for an include value that is not relative |
| N13 | Linux VM | `include.path = ~` | class 1: `"<home>" is not a regular file` |
| N13-b | Linux VM | `include.path = a/../b/inc.cfg`. That file has `path = x.cfg`, and `b/x.cfg` is a FIFO | class 1: `"<repo>/.git/a/../b/x.cfg" is not a regular file` |

### The git calls of a repeated request

Cells N10 sent one request five times.

| System | State | Git calls of `5fd08069`, requests 1 to 5 |
|---|---|---|
| Linux VM, macOS VM, Windows VM | `git.info` with format version `2` | 13, 1, 1, 1, 1. The one call of a later request is the read of the config |
| Linux VM, macOS VM, Windows VM | `git.info` with a config of 20 MiB | 12, 0, 0, 0, 0 |
| Linux VM, macOS VM | `git.info` with a FIFO as the config | 12, 0, 0, 0, 0 |
| Linux VM, macOS VM | `git.info` in a good repository | 21, 8, 8, 8, 8 |

## The third validation round

A fifth round ran the P cells on Linux, macOS and Windows VMs, with the gits of the second validation round. The daemons were `89cb6289`, `5fd08069` and the claustrum of that day. This part gives the answers of `5fd08069`. The part "claustrum" below has the answers of that claustrum.

The macOS VM also ran cells N11, N13-b, N14 and N15-a of the second validation round. `5fd08069` answers there as on the Linux VM.

### Cuts, values and counts

Cells P1, P2 and P3 ran on Linux, macOS and Windows VMs. Cells P7w and P8w ran on the Windows VM.

| Cell | State | `89cb6289` | `5fd08069` |
|---|---|---|---|
| P1 | a format version of 20 characters `7` | the hooks refusal with `bad numeric config value` | the version text with the whole value |
| P1 | a format version of 21 characters `7` | the same | the version text with the first 20 characters, and no mark of the cut |
| P1 | format version `1`, and an extension name of 60 characters `z` | `isRepo` false | the extension text with the whole name |
| P1 | the same with a name of 61 characters | `isRepo` false | the extension text with the first 60 characters, and no mark of the cut |
| P2 | `extensions.worktreeConfig = Yes`, `= On` or `= tRuE`, a linked worktree outside `.claude/worktrees`, and its `config.worktree` has 20 MiB | normal, and `git.worktree_create` makes a worktree | class 1 with the path of that file on `git.info`, `git.status`, `git.list_branches` and `git.worktree_create`. No worktree is made |
| P3 | two included files of 30000 `core.filemode = true` lines | normal | normal |
| P3 | three such included files | normal | the "more settings" text with the path of the third file |
| P3 | an included file of 30000 such lines that includes a second one | normal | normal |
| P3 | a chain of three such files, with the include line at the end of each file | normal | the "more settings" text with the path of the third file |
| P7w | an absolute include value of 299 or 300 characters, and the file is missing | the hooks refusal with `Filename too long` | the Windows text for an include value that is not relative, with the whole value |
| P7w | the same with a value of 301 or 310 characters | the same | that text with the first 300 characters of the value, and no mark of the cut |
| P8w | 16 `include.path` lines of missing files | normal | normal |
| P8w | 17 such lines | normal | the "includes go past" text |

### A remove with `worktreeRoot`

Cells P4 ran on the macOS VM. The worktree is below `worktreeRoot`, and it exists.

| State | `89cb6289` | `5fd08069` |
|---|---|---|
| an `info/exclude` of 20 MiB | `{"success":true}`. The worktree folder and its registration are gone | `{"success":false,"error":"failed to remove worktree: cannot determine the repository's work tree: a file in this repository's .git directory cannot be used: \"<repo>/.git/info/exclude\": it is larger than 4 MiB"}`. The folder and the registration stay |
| a syntax error in line 6 of the config | the work-tree text with the hooks refusal of that line. Nothing is removed | the work-tree text with `this repository's git config cannot be used: fatal: bad config line 6 in \"<repo>/.git/config\"`. Nothing is removed |
| a config of 20 MiB, and `"shadowDirOff":true` | `{"success":true}`. The folder and the registration are gone | the same |
| no refused file, and `"shadowDirOff":true` | `{"success":true}`. The folder and the registration are gone | the same |

### Two refused files of the git directory

Cells P6 ran on the Linux VM, with `git.info` and `git.status`. `git.info` of `89cb6289` is normal in each cell.

| State | `git.status` of `89cb6289` | `5fd08069` |
|---|---|---|
| `info/exclude` is a FIFO, and `info/attributes` has 20 MiB | no answer in 10 s | class 2: `"<repo>/.git/info/attributes": it is larger than 4 MiB` |
| both have 20 MiB | normal | the same text |
| both are a FIFO | no answer in 10 s | class 2: `"<repo>/.git/info/attributes" is not a regular file` |
| `info/exclude` is a directory, and `info/attributes` is a FIFO | no answer in 10 s | the same text |
| `shallow` has 5 MiB of valid ids, and `info/attributes` is a FIFO | no answer in 10 s | class 2: `"<repo>/.git/shallow" is larger than 4 MiB` |
| `shallow` has a text line, and `info/exclude` is a FIFO | `exit status 128` | class 2: `"<repo>/.git/shallow" holds something other than object ids` |

### Ten equal requests

Cells P5 sent one refused request ten times to one daemon. The ten reply lines of a daemon are equal. `git.info` and `git.status` give the same counts.

| System | State | Git calls of `5fd08069`, requests 1 to 10 | Time of `5fd08069` |
|---|---|---|---|
| Linux VM | format version `2` | 13, then 1 at each request, and 2 at the eighth | 175 to 206 ms, then 12 to 36 ms |
| macOS VM, Windows VM | format version `2` | 13, then 1 at each request | 257 to 876 ms, then 17 to 64 ms |
| Linux VM | a config of 20 MiB | 12, then 0, and 1 at the eighth | 174 to 190 ms, then 9 to 37 ms |
| macOS VM, Windows VM | a config of 20 MiB | 12, then 0 | 245 to 728 ms, then 5 to 18 ms |

The first request has a `rev-parse --git-dir` call. With format version `2`, the one call of a later request is the read of the config. On the Linux VM the eighth request came about 6 s after the first, and it has a `rev-parse --git-dir` call again. No git process is left after the tenth request, and none after the stop of the daemon. With format version `2`, `89cb6289` answers `isRepo` false. With the config of 20 MiB its `git.info` is normal after 3.8 to 11.5 s.

### A change of the owner

Cell P7 ran on the Linux VM, 3 times. The repository has format version `2`. Then another user gets it. `git.info` went out at fixed times after that change.

| Time after the change | `5fd08069`, 3 of 3 runs |
|---|---|
| right before it | the class 1 text of the version |
| 0, 1, 2, 3 and 4 s | the same text |
| 5, 6 and 8 s | `isRepo` false |

### A long config path on Windows

Cells P6w and P6x ran on the Windows VM, with a config of 20 MiB, on `git.info` and `git.status`. The repository path is 12 characters shorter than the config path. The lengths 267 and 269 did not run.

| Characters of the config path | Plain `git rev-parse --git-dir` | `89cb6289` | `5fd08069` |
|---|---|---|---|
| 255, 258 | finds the repository | `git.info` with no `branch` member, and `git.status` `isRepo` false | the size text of class 1 |
| 259, 260, 261 | `fatal: not a git repository (or any of the parent directories): .git` | `isRepo` false | the size text of class 1 |
| 262 to 266 | the same text | `isRepo` false | `isRepo` false |
| 268, 270 | `fatal: error reading '<repo>/.git'` | the hooks refusal with that text | `git could not check this repository on the remote machine: at "<repo>": fatal: error reading '<repo>/.git'` |
| 271 | `fatal: cannot change to '<repo>': No such file or directory` | the hooks refusal with `fork/exec <git.exe path>: The directory name is invalid.` | the same form as at 268, with that `fork/exec` text after `at "<repo>": ` |
| 272, 273, 274, 280 | `fatal: cannot change to '<repo>': Filename too long` | the same as at 271 | the same as at 271 |

## claustrum

- The frames of the five methods for format version `2` are the golden `testdata/socket_git_config_refusal.golden.json`.
- From the code: claustrum has each refusal text of this page. The Windows texts are in the Windows build only.
- The rules, the differences from `5fd08069` and the choices of claustrum are in [Repository config refusals](../PROTOCOL.md#repository-config-refusals).
- "The config that the daemon writes for git" in cells E10 and A7b is the copied settings file. The [record of the settings copy](git-settings-copy.md) has its text, and the keys of a `config.worktree` file that it carries.
- Since the settings copy claustrum asks git no question about the owner, and it uses no kept refused read. The lines below on the owner call and on the kept owner answer are of builds before the copy. Rounds `val-s3p2b2-3` and `val-s3p2b2-5` ran a build with the copy. In the cells X9 of round `val-s3p2b2-5` its reply lines equal those of `5fd08069`. That holds in 32 requests on a Linux VM and in 36 on a macOS VM. That record has the cells.
- The third validation round ran a claustrum with the rules of this page, but with no kept owner answer. In the cells P1 to P5 and P6 its reply lines equal those of `5fd08069`, and so does the disk. The same holds in cells P7w and P8w.
- That claustrum ran cells N4, N5, N11, N13-b, N14 and N15-a on Linux and macOS VMs. It ran cells N4, N5 and N11 on a Windows VM. Its reply lines equal those of `5fd08069` there. In cells N14 and N15-a the disk is equal too.
- That claustrum made one `rev-parse` call at each refused request of cells P5 and N10 (Linux, macOS and Windows VMs). With the config of 20 MiB each of the ten requests took 0.7 to 1.1 s. In cell P7 it answered `isRepo` false from 0 s on.
- In cells P6w and P6x that claustrum answered the size text at each length.
- A Linux VM ran a claustrum with the kept owner answer in a fourth round. The cells: P5 with ten requests 1 s apart, P7 in two sequences (3 runs each), N15-a, N15-c and N15-d, two sequences where the `safe.directory` line goes away again, V1, N2, N17, N10, the cells N4, N5, N11, N13-b and N14, and the 463 repeated cells of the Linux VM. Its reply lines equal those of `5fd08069` outside the list of differences, and so does the disk. In cell P7 the two daemons give the same reply at each second. No macOS VM and no Windows VM ran it.
- The last edits after that round changed names and the number of kept verdicts, and they removed one text that the owner question matched. They have tests and ran on no VM.
- The tests of these refusals ran on VMs three times, with no race detector. The first run had the tests of the first form. On a macOS VM: 57 tests, 3 runs, 171 passes. On a Windows VM: 48 tests, 3 runs, no failure.
- The second run had the tests of the validation round. On a macOS VM: 63 tests, 3 runs, 189 passes. On a Windows VM: 54 tests, 3 runs, no failure.
- The third run was on the claustrum of the third validation round. On a macOS VM: 66 tests of the files of these refusals, 3 runs, 198 passes, and 170 git tests in one run. On a Windows VM: 55 tests, 3 runs, no failure. The terminal test is for Linux, and no VM ran it. The test of the kept owner answer ran on Linux only, on no VM.

## Not measured

- The cells P4 on Linux and on Windows: `git.worktree_remove` with `worktreeRoot` and a refused file of the git directory, a syntax error or `"shadowDirOff":true`.
- With `worktreeRoot`: the place of the read among the checks that give the "cannot determine the repository's work tree" text. A config state other than those of cells N14 and P4.
- Without `worktreeRoot`: a refused config together with a leaf that is a symbolic link or not a directory.
- The `config.worktree` test under format version `0` or with no version key, with a file that is large or a FIFO.
- A `shadowDirOff` value other than `true` with a refused config on `git.list_branches`, `git.worktree_create` and `git.worktree_remove`.
- The name bound in a section beyond the measured ones.
- The count of settings in a section beyond the twelve counted ones, `x` and `user`. The exact count beyond `core` and `remote`.
- A format version value or an extension name with characters of two bytes.
- Another value of one of the ten known extensions, beyond the values of the tables.
- `extensions.worktreeConfig` in a letter case beyond `true`, `TRUE`, `on`, `ON`, `Yes`, `On` and `tRuE`.
- An `includeIf` section in an included file.
- An included file past the 16 that also cannot be opened.
- The order of the two bounds and an include problem, beyond the two cells of 200000 include lines.
- 16 `include.path` lines of missing files on macOS.
- The kind test and the size test of the `config.worktree` file of the main worktree.
- Content of the file of an `includeIf` section, beyond a format version `2`. Content of a `config.worktree` file beyond that value and the keys in the record of the settings copy. An open error at such a file.
- A read of the config that does not finish, and a config that changes its size during the read.
- Which change of a file makes `5fd08069` read the config again, beyond the one changed config of cell B4. A rewrite of the same size with the file time set back.
- Cell P7 on macOS and Windows. The 5 seconds of that cell count from the end of the call about the repository (cell 34 of round `s3p2b-edges`, Linux VM).
- A config over its size bound that includes a FIFO, in a repository of the daemon's user.
- A config whose listing by git has more than 64 MiB. From the code: claustrum tests the name bound and the count of settings on the first 64 MiB. If neither refuses, its read has no verdict.
- An error of the daemon itself in an open, such as a full file table.
- A bare repository with a refused config on a host where git refuses bare repositories.
- `HOME` unset with `include.path = ~/x`: no daemon starts with no home folder.
- Two refused states in one config file.
- A daemon with `GIT_DIR` or `GIT_COMMON_DIR` in its environment.
- A git between 2.12.5 and 2.13.7, and a git before 2.11.4.
- An open error of `shallow` other than mode 000 and a socket.
- A symbolic link to a FIFO or to a large file as `info/attributes` or `info/exclude`.
- On macOS: the cells V4, V5 and V18, and the socket cells of V7 and V10. Of the cells N11 to N17, the cells N12, N13 with `~`, N15-b, N16 and N17 did not run there. Nor did the cells P6 and P7.
- On Windows: the cells V4, V5, V13, V17 and V18, and the cells X1, X2, X3 and X9 of the edge round. No FIFO, socket or mode 000 cell ran there. Of the cells N11 to N17, the cells N11w, N12w and N17w ran. The cells P6 and P7 did not run there.
- On Windows: a config path of 267 or 269 characters with a config of 20 MiB.
- On Windows: a link above the config, and a link at an included file.
- On Windows: a relative include that leaves the repository through a junction inside it.
- On Windows: an include value that starts with a slash or a drive.
