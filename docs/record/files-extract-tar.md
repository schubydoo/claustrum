# files.extract_tar: measurement record

This page is a record, not reading material. It holds the measurements behind the modes, the unsafe names, the marker and the error texts of `files.extract_tar`. A new measurement of the method goes into this page. The tests in `extract_execbit_unix_test.go` and `extract_collision_test.go` pin rows of the tables, and the section [claustrum](#claustrum) says which.

To use the method, read [files.extract_tar](../protocol/files-extract-tar.md).

`5fd08069` was measured on a Linux VM, a macOS VM and a Windows VM, beside
`89cb6289`. The two builds differ in the rows that the tables mark.

In each row of this page the frame has the form
`{"jsonrpc":"2.0","id":1,"result":{…}}`. A failure frame is
`{"success":false,"fileCount":0,"error":"<text>"}`, and the tables give the
text.

## The modes of the extracted entries

### Linux and macOS

One archive holds nine files and three directories. `destDir` did not exist
before the request. Both builds answer `{"success":true,"fileCount":9}`, and
the archive is gone after the request.

| entry, with its mode in the archive | `5fd08069` | `89cb6289` |
|---|---|---|
| file `0644` | `0600` | same |
| file `0600` | `0600` | same |
| file `0444` | `0600` | same |
| file `0755` | `0700` | `0600` |
| file `0700` | `0700` | `0600` |
| file `0111` | `0700` | `0600` |
| file `04755` | `0700` | `0600` |
| file `02755` | `0700` | `0600` |
| file `01777` | `0700` | `0600` |
| directory `0755`, directory `0700`, directory `0555` | `0700` | same |
| `destDir` itself | `0700` | same |
| `.synced` | `0600`, 0 bytes | same |

More facts of these rows:

- The Linux cells are `2a1-modes-u022`, `2a1-modes-u077` and `2a1-modes-u000`.
  The daemon umask was 022, 077 and 000. The three cells give the same modes.
- The macOS cells are `2a-modes-u022` and `2a-modes-u077`, with the daemon
  umask 022 and 077. Both give the modes of the table.
- No setuid, setgid or sticky bit arrived on either build, on both VMs.
- A second archive adds a file `0644` inside each directory (Linux cells
  `2a1-modesin-*`, macOS cells `2a-nested-*`). Each of these files is `0600` on
  both builds.
- On the Linux VM the second archive also holds a file `deep/er/f` of mode
  `0755`, and no directory entry for `deep` or `deep/er`. That file is `0700` on
  `5fd08069` and `0600` on `89cb6289`. The two directories are `0700` on both.
  Both builds answer `{"success":true,"fileCount":13}`.

The execute bit of the owner decides. A third archive of the Linux VM holds
twelve files, and a fourth holds the same files inside a directory entry. Both
give the same modes. The macOS VM gives the same modes at the top and inside a
directory:

| mode in the archive | `5fd08069` | `89cb6289` |
|---|---|---|
| `0100`, `0744`, `0755` | `0700` | `0600` |
| `0010`, `0001`, `0011`, `0654`, `0645`, `0610`, `0601`, `0674`, `0644` | `0600` | same |

### Windows

The archive of cell `2a-modes` holds the files `f644.txt` (`0644`), `f755.sh`
(`0755`) and `f444.txt` (`0444`), and the directories `d755` and `d555`. Cell
`2a-modes-nested` adds three files inside the directories.

| | `5fd08069` | `89cb6289` |
|---|---|---|
| frame, `2a-modes` | `{"success":true,"fileCount":3}` | same |
| frame, `2a-modes-nested` | `{"success":true,"fileCount":6}` | same |
| read-only attribute | on no entry | same |
| `icacls` text of each entry | inherited entries | the same text |

So the execute bit of the archive gave no difference between the two builds on
the Windows VM. These two observables cannot carry an execute bit. Two passes
gave the same listings.

## A destDir that the daemon cannot search

The daemon ran with umask 0100 (Linux cell `2a1-modes-u0100`, macOS cell
`2a-modes-u100`). The new `destDir` then has mode `0600`. `destDir` was
`<home>/x/dest` on the Linux VM and `<home>/dest` on the macOS VM.

| | `5fd08069` | `89cb6289` |
|---|---|---|
| text, Linux VM | `open destDir: "dest" could not be examined (statat .: permission denied)` | `create f0644: open <destDir>/f0644: permission denied` |
| text, macOS VM | the same text | `create f0644: open <destDir>/f0644: permission denied` |
| `destDir` after the request | mode `0600`, no entry | same |
| the archive after the request | gone | same |

## Two entries that collide

Each archive holds two entries that collide. On the Linux VM a third entry
`c.txt` follows, and it did not arrive on either build. Before each request
`destDir` held `old.txt` and `.synced`. After each request the archive and that
old content are gone on both builds. `destDir` then holds the first entry of the
archive and no `.synced`.

A file entry `a`, then an entry `a/b`:

| system and cell | `5fd08069` | `89cb6289` |
|---|---|---|
| Linux VM, `2a2-10-file-then-below` | `mkdir parent a/b: mkdirat a: file exists` | `mkdir parent a/b: mkdir <destDir>/a: not a directory` |
| macOS VM, `2a-file-then-below` | `mkdir parent a/b: mkdirat a: file exists` | `mkdir parent a/b: mkdir <destDir>/a: not a directory` |
| Windows VM, `2a-file-then-below` | `mkdir parent a/b: mkdirat a: file exists` | `mkdir parent a/b: mkdir <destDir>\a: The system cannot find the path specified.` |

A directory entry, then a file entry with that name. The name is `x` on the
Linux VM and `d` on the macOS and Windows VMs:

| system and cell | `5fd08069` | `89cb6289` |
|---|---|---|
| Linux VM, `2a2-11-dir-then-file` | `create x: openat x: file exists` | `create x: open <destDir>/x: is a directory` |
| macOS VM, `2a-dir-then-file` | `create d: openat d: file exists` | `create d: open <destDir>/d: is a directory` |
| Windows VM, `2a-dir-then-file` | `create d: openat d: is a directory` | `create d: open <destDir>\d: is a directory` |

More collisions, from a later round. `<destDir>` stands for the whole path:

| archive | system | `5fd08069` | `89cb6289` |
|---|---|---|---|
| file `a`, file `a/b/c` | Linux and macOS VMs | `mkdir parent a/b/c: openat a: not a directory` | `mkdir parent a/b/c: mkdir <destDir>/a: not a directory` |
| the same | Windows VM | `mkdir parent a/b/c: openat a: The system cannot find the path specified.` | `mkdir parent a/b/c: mkdir <destDir>\a: The system cannot find the path specified.` |
| file `s/a`, file `s/a/b` | Linux and macOS VMs | `mkdir parent s/a/b: mkdirat s/a: file exists` | `mkdir parent s/a/b: mkdir <destDir>/s/a: not a directory` |
| the same | Windows VM | `mkdir parent s/a/b: mkdirat s\a: file exists` | `mkdir parent s/a/b: mkdir <destDir>\s\a: The system cannot find the path specified.` |
| directory `s/d/`, file `s/d` | Linux and macOS VMs | `create s/d: openat s/d: file exists` | `create s/d: open <destDir>/s/d: is a directory` |
| the same | Windows VM | `create s/d: openat s\d: is a directory` | `create s/d: open <destDir>\s\d: is a directory` |
| a file entry `.` | Linux and macOS VMs | `create .: openat .: file exists` | `create .: open <destDir>: is a directory` |
| the same | Windows VM | `create .: openat .: is a directory` | `create .: open <destDir>: is a directory` |
| a file name of 300 characters | Linux and macOS VMs | `create <name>: openat <name>: file name too long` | `create <name>: open <destDir>/<name>: file name too long` |
| the same | Windows VM | `create <name>: openat <name>: The filename, directory name, or volume label syntax is incorrect.` | the same reason after `open <destDir>\<name>:` |
| daemon umask 0200, so `destDir` has mode `0500` | Linux and macOS VMs | `create a.txt: openat a.txt: permission denied` | `create a.txt: open <destDir>/a.txt: permission denied` |
| directory `a.txt/`, file `a.txt` | Linux VM | `create a.txt: openat a.txt: file exists` | `create a.txt: open <destDir>/a.txt: is a directory` |
| a file entry `/`, and one named `//` | Linux VM | `create /: openat .: file exists`, and the same with `//` | `create /: open <destDir>: is a directory`, and the same with `//` |
| file `...`, and a file named `.` with a space after it | Windows VM | `create ...: openat ...: is a directory`, and the same with the other name | `create ...: open <destDir>\...: is a directory`, and the same with the other name |
| file `x/.../y.txt` | Windows VM | `create x/.../y.txt: openat x\...\y.txt: The system cannot find the path specified.` | the same reason after `open <destDir>\x\...\y.txt:` |
| directory `d/`, file `D` | macOS VM | `create D: openat D: file exists` | `create D: open <destDir>/D: is a directory` |
| a file entry `/`, and one named `\` | Windows VM | `create /: openat .: is a directory`, and the same with `\` | `create /: open <destDir>: is a directory`, and the same with `\` |

## Names that leave destDir

The refusal is `unsafe path in archive: <name>` with `fileCount` 0. An entry
before the refused one stays on disk.

| entry | system | `5fd08069` | `89cb6289` |
|---|---|---|---|
| file `../evil` | Linux, macOS and Windows VMs | refused | same |
| file `sub/../../dest`, after a directory `sub/` | Linux, macOS and Windows VMs | refused | `create sub/../../dest: open <destDir>: is a directory` |
| file `../dest/x.txt` | Linux, macOS and Windows VMs | refused | success, the file lands as `x.txt` |
| file `sub/../../dest/y.txt`, after a directory `sub/` | Linux, macOS and Windows VMs | refused | success, the file lands as `y.txt` |
| directory `../dest/dd/` | Linux, macOS and Windows VMs | refused | success, the directory lands as `dd` |
| file `/abs/../../x.txt`, file `/../y.txt` | Linux VM | refused | same |
| file `a/../b.txt` | Linux, macOS and Windows VMs | success, the file lands as `b.txt`, no folder `a` | same |
| file `a/./b/../c.txt` | Linux VM | success, the file lands as `a/c.txt`, no folder `b` | same |
| file `d/../a` | Linux VM | success | same |
| file `./c.txt` | Linux, macOS and Windows VMs | success | same |
| a directory entry `.`, and one named `./` | Linux, macOS and Windows VMs | success | same |
| a directory entry `/`, and one named `//` | Linux VM | success | same |
| an absolute file name | Linux VM | success, the file lands under `destDir` | same |
| file `NUL`, file `COM1`, file `aux` | Windows VM | refused | same |
| file `NUL/x.txt`, file `x/NUL`, file `NUL.`, file `NUL` with a space after it | Windows VM | refused | same |
| file `CONIN$`, file `CONOUT$`, file `COM¹` | Windows VM | refused | same |
| directory `NUL/`, directory `a:b/` | Windows VM | refused | same |
| file `a:b`, file `x/a:b`, file `a:b/c.txt` | Windows VM | refused, no stream | same |
| file `\..\y.txt`, file `x\..\..\y.txt` | Windows VM | refused | same |
| file `a.txt::$DATA` | Windows VM | refused | same |
| file `C:x.txt`, file `C:\o\evil.txt` | Windows VM | refused | same |
| file `..\evil2` | Windows VM | refused | same |
| file `\\srv\share\f.txt` | Windows VM | success, the file lands as `srv\share\f.txt` | same |
| file `x\y.txt` | Windows VM | success, a folder `x` with `y.txt` | same |
| file `a.txt.`, and file `a.txt` with a space after it | Windows VM | success, the file lands as `a.txt` | same |
| file `CON.txt`, file `nul.txt`, file `nul.tar.gz`, file `LPT1.txt`, file `com0` | Windows VM | success | same |

In these rows `destDir` ends with `dest`. The Windows VM runs Windows 11.

The rule of claustrum is fitted to these rows. It cuts the leading separators
of the name. The rest must be a local path for the Go library
(`filepath.IsLocal`). That test is lexical. On Windows it refuses a reserved
device name and a name with `:`.

claustrum before this change had the prefix test alone. On a Windows VM it
answered success for the entry `NUL`, with no such entry on disk. For the entry
`a:b` it answered success too, and it wrote a stream `b` on a file `a`.

## Two file entries with one name

Each row is a success, and `fileCount` counts each entry. The content is that
of the last entry.

| entries of `a.txt` | system | mode on `5fd08069` | mode on `89cb6289` |
|---|---|---|---|
| `0644`, then `0755` | Linux and macOS VMs | `0700` | `0600` |
| `0755`, then `0644` | Linux and macOS VMs | `0600` | same |
| `0644`, `0755`, `0644` | Linux and macOS VMs | `0600` | same |
| `a.txt` `0644`, then `./a.txt` `0755` | Linux and macOS VMs | `0700` | `0600` |
| 10 bytes, then 3 bytes, both `0644` | Linux and macOS VMs | `0600`, the file has the 3 bytes | same |

On the Linux VM two entries `a.txt` of 5 MiB each, `0644` and then `0755`,
leave the content of the second entry with mode `0700` on `5fd08069`.

Rows of the Windows VM and of the macOS VM. Both volumes ignore letter case.
Each row is a success, and `fileCount` counts each entry:

| entries | system | `5fd08069` | `89cb6289` |
|---|---|---|---|
| `a.txt` with 10 bytes, then with 3 bytes | Windows VM | `a.txt` has the 3 bytes | same |
| `a.txt`, then `./a.txt` | Windows VM | `a.txt` has the content of the second entry | same |
| `a.txt`, then `A.TXT` | Windows and macOS VMs | a file `A.TXT` with the content of the second entry | a file `a.txt` with that content |
| `a.txt`, `A.txt`, then `a.TXT` | Windows and macOS VMs | a file `a.TXT` with the content of the third entry | a file `a.txt` with that content |
| directory `d/`, directory `D/`, file `D/f.txt` | macOS VM | a folder `d` with `f.txt` | same |

A new archive over an old `destDir`: the old `a.txt` had mode `0644`, and the
archive has `a.txt` with `0755`. The file is `0700` on `5fd08069` and `0600` on
`89cb6289` (Linux VM).

## Directory entries

| archive | system | `5fd08069` | `89cb6289` |
|---|---|---|---|
| file `a`, directory `a/` | Linux and macOS VMs | `mkdir a/: mkdirat a: file exists` | `mkdir a/: mkdir <destDir>/a: not a directory` |
| the same | Windows VM | `mkdir a/: mkdirat a: file exists` | `mkdir a/: mkdir <destDir>\a: The system cannot find the path specified.` |
| file `a`, directory `a/b/` | Linux and macOS VMs | `mkdir a/b/: openat a: not a directory` | `mkdir a/b/: mkdir <destDir>/a: not a directory` |
| the same | Windows VM | `mkdir a/b/: openat a: The system cannot find the path specified.` | `mkdir a/b/: mkdir <destDir>\a: The system cannot find the path specified.` |
| file `a.txt`, directory `a.txt/` | Linux VM | `mkdir a.txt/: mkdirat a.txt: file exists` | `mkdir a.txt/: mkdir <destDir>/a.txt: not a directory` |
| directory `d/` two times | Linux, macOS and Windows VMs | success | same |
| directory `p/q/r/` with no entry for `p` or `p/q` | Linux, macOS and Windows VMs | success. On Linux and macOS the three folders are `0700` | same |
| directory `d/` of mode `0000` with a file `d/f` | Linux and macOS VMs | success, `d` is `0700` and holds `f` | same |
| directory `e/` of mode `0555` with a file `e/f` | Linux VM | success, `e` is `0700` and holds `f` | same |

Each failure of the table has `fileCount` 0 on both builds, and the file `a`
stays.

## The marker

After the entries, `.synced` at the top of `destDir` is an empty file. The two
builds are equal in each row, in the reply and on disk.

| archive | system | reply | `.synced` afterwards |
|---|---|---|---|
| directory `.synced/` alone | Linux, macOS and Windows VMs | success, `fileCount` 0 | an empty file |
| file `a.txt`, directory `.synced/` | Linux, macOS and Windows VMs | success, `fileCount` 1 | an empty file |
| directory `.synced/`, file `.synced/in.txt` | Linux, macOS and Windows VMs | success, `fileCount` 1 | an empty file. `in.txt` is gone |
| directory `.synced/`, directory `.synced/deep/`, file `.synced/deep/f` | Linux, macOS and Windows VMs | success, `fileCount` 1 | an empty file. `deep` and `f` are gone |
| file `.synced` with 13 bytes, of mode `0755` on Linux and macOS | Linux, macOS and Windows VMs | success, `fileCount` 1 | an empty file |
| directory `sub/.synced/` with a file in it | Linux, macOS and Windows VMs | success, `fileCount` 1 | an empty file at the top. `sub/.synced` stays a folder with its file |
| an old `destDir` whose `.synced` is a folder with a file, and a good archive | Linux, macOS and Windows VMs | success | an empty file. The old folder is gone |
| directory `.SYNCED/`, file `.SYNCED/in.txt` | Windows and macOS VMs | success, `fileCount` 1 | an empty file named `.synced`. `in.txt` is gone |
| an old `destDir` whose `.synced` is a symbolic link to a folder outside, and a good archive | Linux VM | success | an empty real file. The folder outside is not changed |

On the Linux and macOS VMs the marker has mode `0600` in each of their rows.

One more row of the Linux and macOS VMs is equal on both builds. The old
`destDir` holds a symbolic link `lnk` to a folder outside, and the archive has a
file `lnk/f.txt`. The request succeeds. `lnk` is then a real folder with
`f.txt`, and the folder outside is not changed.

## The preparation of destDir

After the gzip header the daemon prepares `destDir`. `5fd08069` was measured
beside `89cb6289` on Linux, macOS and Windows VMs. The answers of `5fd08069` on
the Linux and macOS VMs give this order: the missing folders above `destDir`,
the open of the parent folder, the wipe of the old `destDir`, the new
`destDir`, and its open. claustrum does the steps in that order (from the
code).

In no Linux or macOS cell of these rounds did a file or a folder outside
`destDir` lose content, on either build. The same holds for the Windows cells
of the parent, of the wipe, of the old content and of what `destDir` is before.
The Windows spelling rows are apart: see "Spellings of destDir". The new
folders above `destDir` are the one kind of new entry outside it.

### The parent folder of destDir

`<parent>` is the whole path of the parent. Linux and macOS VMs:

| mode of the parent | `destDir` before | `5fd08069` | `89cb6289` | disk afterwards |
|---|---|---|---|---|
| `0700` | present, or absent | success | same | the new content |
| `0500` | present, with old content | `clean destDir: RemoveAll dest: permission denied` | `clean destDir: unlinkat <destDir>: permission denied` | both: `destDir` stays and is empty |
| `0500` | absent | `mkdir destDir: mkdirat dest: permission denied` | `mkdir destDir: mkdir <destDir>: permission denied` | both: no `destDir` |
| `0300` | present | `open parent: open <parent>: permission denied` | `clean destDir: open <parent>: permission denied` | both: the old content stays |
| `0300` | absent | `open parent: open <parent>: permission denied` | success | `5fd08069`: no `destDir`. `89cb6289`: the new content |
| `0100` | present | `open parent: open <parent>: permission denied` | `clean destDir: open <parent>: permission denied` | both: the old content stays |
| `0100` | absent | `open parent: open <parent>: permission denied` | `mkdir destDir: mkdir <destDir>: permission denied` | both: no `destDir` |
| `0000` | present | `open parent: open <parent>: permission denied` | `clean destDir: open <parent>: permission denied` | both: the old content stays |
| `0000` | absent | `open parent: open <parent>: permission denied` | `clean destDir: open <parent>: permission denied` | both: no `destDir` |

The mode of the parent is the same after each request.

A parent that the daemon can read and cannot search (Linux and macOS VMs). The
rows are a parent of mode `0400` with `destDir` present and absent, and a
parent of mode `0600` with `destDir` present. Nothing changes on disk in the
three rows:

| build | text |
|---|---|
| `5fd08069` | `open parent: open <parent>: permission denied` |
| `89cb6289` | `clean destDir: openfdat <destDir>: permission denied` |

claustrum answers the text of `5fd08069` in the three rows (Linux VM). On the
macOS VM the rows ran on a build of claustrum before the fix of this text. It
answered `clean destDir: RemoveAll dest: permission denied` there.

Two more rows of the Linux VM. With a parent of mode `0500` and `destDir`
present, `5fd08069` and claustrum answer `clean destDir: RemoveAll dest:
permission denied`, and `destDir` stays and is empty. For a last name of 256
bytes they answer `clean destDir: RemoveAll <name>: file name too long`, and
nothing is made. `89cb6289` answers `clean destDir: unlinkat <path>: …` with
the same reasons in the two rows.

With a daemon umask of 0400 each new folder has mode `0300` (Linux and macOS
VMs):

| case | `5fd08069` | `89cb6289` |
|---|---|---|
| `destDir` does not exist | `open destDir: openat dest: permission denied`. `destDir` is empty afterwards | success. Each new file has mode `0200` |
| two folders above `destDir` do not exist either | `open parent: open <parent>: permission denied`. The two folders exist afterwards, and no `destDir` | success |

Missing folders above `destDir` appear with mode `0700` on both builds, for
one, two and three missing folders (Linux and macOS VMs). The Windows VM shows
the same folders.

A folder above `destDir` that cannot be made answers `mkdir parent: mkdir
<path>: <reason>` on both builds, with the whole path of the folder that fails
(Linux VM). Nothing changes on disk, and an old `destDir` keeps its content:

| case | `destDir` below a folder `<x>` | text after `mkdir parent: ` |
|---|---|---|
| the parent is a regular file | `f.txt/dest` | `mkdir <x>/f.txt: not a directory` |
| a missing chain below a file | `f.txt/m1/dest` | `mkdir <x>/f.txt: not a directory` |
| `<x>/g` has mode `0600`, with `destDir` present and absent | `g/p/dest` | `mkdir <x>/g/p: permission denied` |
| `<x>/ro` has mode `0500`, and the parent does not exist | `ro/m1/dest` | `mkdir <x>/ro/m1: permission denied` |
| the parent is a dangling symbolic link | `dlink/dest` | `mkdir <x>/dlink: file exists` |

A build of this change before the `mkdir parent:` prefix answered these rows
too, with the prefix `mkdir destDir:` (Linux VM).

On the Linux and macOS VMs both builds and claustrum answer the same prefix in
three rows.
A parent that is a regular file, and a missing chain below a file, answer
`mkdir parent: mkdir <path of the file>: not a directory`. A missing folder
below a folder of mode `0500` answers `mkdir parent: mkdir <path>: permission
denied`. Nothing changes on disk.

On the Windows VM a parent that is a file, and a missing chain below a file,
answer `mkdir parent: mkdir <path of the file>: The system cannot find the path
specified.` on both builds.

### A wipe that fails

`5fd08069` answers `clean destDir: RemoveAll dest: <reason>`. `89cb6289` names
the entry that stopped it, with the whole path. The disk is equal on both
builds in each row: the entries that can go are gone, and `destDir` is the
folder that it was.

| old content of `destDir` | system | reason | text of `89cb6289` after `clean destDir: ` | what stays |
|---|---|---|---|---|
| a subfolder of mode `0000` with a file | Linux and macOS VMs | `permission denied` | `openfdat <destDir>/<sub>: permission denied` | the subfolder and its file. The other old entries are gone |
| a subfolder of mode `0500` with a file | Linux and macOS VMs | `permission denied` | `unlinkat <destDir>/<sub>/in.txt: permission denied` | the subfolder and its file |
| `destDir` itself has mode `0000`, with content | Linux and macOS VMs | `permission denied` | `openfdat <destDir>: permission denied` | all of the old content |
| `destDir` itself has mode `0300` or `0500`, with content | Linux and macOS VMs | `permission denied` | `openfdat <destDir>: …`, and `unlinkat <destDir>/old.txt: …` | all of the old content |
| a file with the `uchg` flag, and a folder with that flag and a file | macOS VM | `operation not permitted` | `unlinkat <path>: operation not permitted` | the flagged entry, and the file in the flagged folder |
| a file that another process holds open with no sharing | Windows VM | `The process cannot access the file because it is being used by another process.` | `unlinkat <destDir>\a.txt:` and the same reason | the held file |
| a folder that another process holds open, and one that is its working directory | Windows VM | the same reason | `unlinkat <destDir>\<folder>:` and the same reason | the folder, empty |
| the archive itself is a file in `destDir` | Windows VM | the same reason | `unlinkat <destDir>\in.tgz:` and the same reason | `destDir`, empty |

On the Linux and macOS VMs the request with the archive inside `destDir`
succeeds on both builds.

### Old content that goes

Each row is a success on both builds, and the old content is gone:

- Linux and macOS VMs: three levels of folders with files, a file of mode
  `0000`, a dangling symbolic link, a FIFO, and a file that another process
  holds open.
- Linux VM: a symbolic link loop, a bound socket, 2000 files, and a subfolder
  that is the working directory of a process. That process lives on.
- Linux and macOS VMs: a symbolic link to a folder outside, a symbolic link to
  a file outside, and a hard link to a file outside. The folder and the files
  outside keep their content. The hard-linked file has one link less.
- Windows VM: three levels of folders, a file that is read-only, hidden and
  system, a read-only folder with content, a dangling junction, a file with a
  second stream, a file held with share delete, and a path of 302 characters.
- Windows VM: a junction, a directory link and a file link to a place outside.
  That place keeps its content.

### What destDir is before the request

Each row is a success on both builds, and `destDir` is a real folder
afterwards:

- Linux and macOS VMs: a regular file, a symbolic link to a folder outside, a
  dangling symbolic link, and a FIFO. The folder outside keeps its content.
- Linux VM: a symbolic link to a file outside, and an empty folder of mode
  `0000`.
- Windows VM: a regular file, a junction to a folder outside, a dangling
  junction, a directory link, and a read-only empty folder. The folder outside
  keeps its content.

### The new folder

After a good wipe `destDir` is a new folder on both builds, not the old one
emptied. On the Linux VM a handle that the driver held on the old folder has
no link afterwards. On the macOS VM the inode is new. On the Windows VM the
file id and the creation time are new.

The new folder has mode `0700` on both builds (Linux VM):

| daemon umask | mode of the old `destDir` | mode of the new `destDir` |
|---|---|---|
| 077 | `0755`, `0777` | `0700` |
| 000 | `0755`, `0777` | `0700` |
| 022 | `0755` | `0700` |

The folder takes the spelling of the request. A request for `DEST` over a
folder `dest` leaves a folder named `DEST` on both builds (macOS and Windows
VMs).

After a wipe that failed, and after a failure before the wipe, `destDir` is the
same folder as before.

### The order of the steps

Equal on both builds:

| case | system | reply | old content |
|---|---|---|---|
| the archive does not exist | Linux, macOS and Windows VMs | `open archive: …` | stays |
| the archive is not gzip | Linux, macOS and Windows VMs | `gzip: gzip: invalid header` | stays |
| gzip data that is not a tar | Linux VM | `tar read: archive/tar: invalid tar header` | gone. `destDir` is a new empty folder |
| a relative `destDir` | Linux VM | `destDir must be an absolute, non-root path: …` | stays, and the archive stays |

## Spellings of destDir

Each row is a success on the builds that the last column names:

| `destDir` | system | builds |
|---|---|---|
| a separator after the last name | Windows VM | `89cb6289` and `5fd08069` |
| a separator after the last name, `/./` inside the path, a parent folder that is a symbolic link | Linux VM | `5fd08069` |
| a `..` inside the path, a doubled separator | Linux VM | `5fd08069` |
| a parent folder that is a symbolic link to a folder | Linux VM | `89cb6289` and `5fd08069` |
| a `..` part after a part that is a symbolic link | Linux VM | `89cb6289` and `5fd08069` |
| a path through the `/tmp` link | macOS VM | `89cb6289` and `5fd08069` |
| forward slashes, another letter case, a short name, and the `\\?\` prefix | Windows VM | `89cb6289` and `5fd08069` |
| `dest\.` and `dest\..\dest` | Windows VM | `89cb6289` and `5fd08069` |
| a `..` inside the path, and a `destDir` two new folders deep | Windows VM | `5fd08069` |
| `destDir` is a symbolic link to a folder | Linux, macOS and Windows VMs | `89cb6289` and `5fd08069` |
| `destDir` is a junction to a folder | Windows VM | `89cb6289` and `5fd08069` |

In the two last rows `destDir` is a real folder afterwards, and the target of
the link keeps its content. With another letter case or a short name, the new
folder has the name as the request spelled it (Windows VM).

Old content that is read-only is replaced on both builds (Windows VM): a
read-only file at the name of an entry, a read-only folder, and a read-only
folder at the name of a file entry.

For `<x>/a/link/../dest` with `link` to `<x>/t/sub` both builds act on
`<x>/a/dest`,
and `<x>/t/dest` is not changed (Linux VM).

A `destDir` that ends in `/.` or in `/..` is a success on both builds and on
claustrum (Linux and macOS VMs). They wipe the folder that the cleaned path names and
extract there: `<x>/dest` for `<x>/dest/.`, and `<x>/a` for `<x>/a/b/..`.

A `destDir` that is a symbolic link to a folder outside, with a separator after
its name, is a success on both builds and on claustrum (Linux VM). `destDir` is
a real folder afterwards, and the folder outside keeps its file.

A `destDir` with the last name `.synced` gets the marker file inside it on
`5fd08069` (Linux and Windows VMs).

On the Windows VM four last names mean the folder `dest`: `dest.`, `dest` with
a space after it, `dest..` and `dest. .`. The folder on disk keeps the name
`dest` on both builds.

| last name | `5fd08069` | `89cb6289` |
|---|---|---|
| `dest.` | success. The old content is gone, and the folder is new | success. The old file stays beside the new one, in the old folder |
| the three other names | success, as for `dest.` | `create ok.txt: open <destDir>\ok.txt: The system cannot find the path specified.` Nothing is wiped |

claustrum before this change answered success in these four rows and kept the
old file beside the new one. For `dest\.` it failed with `clean destDir: RemoveAll <destDir>: invalid argument`
and wiped nothing, where both builds extract (Windows VM).

### Windows paths with the `\\?\` prefix

With this prefix Windows does not drop a dot or a space after a name. The rows
are from a Windows VM. `dest` holds old content before each request.

| `destDir` after the prefix | `5fd08069` | `89cb6289` |
|---|---|---|
| `dest.`, `dest` with a space after it, `dest..`, with no entry of that exact name | success. `dest` is wiped and filled | success. A new folder with the exact name gets the files, and `dest` keeps its content |
| `dest.` or `dest` with a space, and an entry of that exact name exists too | success. `dest` is wiped and filled, and the entry with the exact name keeps its content | success. The entry with the exact name is wiped and filled, and `dest` keeps its content |
| `dest\.`, `DEST`, a short name | success. The folder that the name means is wiped and filled | same |

A `destDir` of the forms `\\.\C:\…\dest` and `\\localhost\C$\…\dest` is a
success on both builds, with `dest` wiped and filled.

With no prefix, and with both `dest` and an entry named `dest.` on disk, a
`destDir` of `dest.` wipes and fills `dest` on `5fd08069`. On `89cb6289` the
entry `dest.` goes, and `dest` keeps its old file and gets the new one.

A third state ran for `dest.` and for `dest` with a space: no `dest` and no
entry of the exact name. `89cb6289` makes a folder with the exact name and
extracts there. `5fd08069` makes `dest` and extracts there (Windows VM).

claustrum refuses the rows of the first two table lines. It answers
`clean destDir: "<name>" is not the entry that the path names`, and it deletes
nothing and makes no `destDir` (Windows VM, for `dest.` and for `dest` with a
space). In the third state it answers the same text
after `mkdir destDir:`, and no folder stays (Windows VM). The name `dest..` did
not run on claustrum: its answer is from the code.

The row with no prefix is
equal on claustrum and on `5fd08069` (Windows VM, on a build before the test of
the two views).

A build of claustrum before the test of the two views emptied `dest` in the
rows of the first two table lines and then failed with
`open destDir: "<name>" changed while it was opened` (Windows VM).

### Last names that get a `RemoveAll <name>` refusal

On the Windows VM `5fd08069` answers `clean destDir: RemoveAll <name>: path
escapes from parent` for the last names `NUL`, `a:b`,
`dest::$INDEX_ALLOCATION`, `CON` and `COM1`. For `dest?` and `dest*` it answers
`clean destDir: RemoveAll <name>: The filename, directory name, or volume label
syntax is incorrect.` Nothing changes on disk in these rows. `89cb6289` answers
another failure text for `NUL`, `a:b`, `dest?` and `dest*`. It extracts for
`dest::$INDEX_ALLOCATION`, and it makes a folder for `CON` and for `COM1`.
claustrum answers as `5fd08069` in the seven rows (Windows VM).

A last name of dots and spaces alone (Windows VM, two passes). The parent
folder `p` holds content before each request:

| last name | `5fd08069` and claustrum | `89cb6289` |
|---|---|---|
| `...`, one space, `. .`, `....`, and `...` with a space after it | `clean destDir: RemoveAll <name>: invalid argument`. `p` keeps its content | `create ok.txt: open <destDir>\ok.txt: The system cannot find the path specified.` `p` keeps its content |
| `...` behind the `\\?\` prefix | `5fd08069`: `clean destDir: RemoveAll ...: invalid argument`. claustrum: `clean destDir: "..." is not the entry that the path names`. `p` keeps its content on both | success. A folder with the exact name `...` is made in `p` and gets the files. The old content of `p` stays |

Two more rows of that round act on the cleaned path, which is the path that
the home guard judged. They are equal on both builds and on claustrum (Windows
VM). For `<p>\.` each wipes `p` and extracts there. For `<p>\..` each empties
the folder above `p` and then fails, because the archive in that folder is
open. `5fd08069` and claustrum answer `clean destDir: RemoveAll <name>: The
process cannot access the file because it is being used by another process.`
`89cb6289` answers `clean destDir: unlinkat <path of the archive>:` and the
same reason.

## Rows where the two builds are equal

These cells ran on `5fd08069` and on `89cb6289` in the same rounds. The frame
and the disk are equal on both builds in each of them:

- Linux, macOS and Windows VMs: a good archive, no archive, a file that is not
  gzip, gzip data that is not a tar, a symbolic link entry, and an entry
  `../evil`.
- Linux VM: an empty file, a hard link entry, a `destDir` that is a file, a
  relative `destDir`, one file of 20 MiB and 2000 files.
- Linux VM: the 13 archives of the cells for the `tar read:` and
  `write <entry>:` texts. Four of them extract. The rows of `5fd08069` for
  these archives are from the validation rounds.

## claustrum

The tests of claustrum pin these rows:

- Linux and macOS: each mode row of the two mode tables, at the top and inside
  a directory, and the file `deep/er/f` with its two directories
  (`TestFilesExtractTarKeepsExecuteBit`).
- Linux and macOS: the frame and the disk of the umask 0100 row
  (`TestFilesExtractTarDestDirNotSearchable`).
- Linux, macOS and Windows: the frames of the two first collisions, each with
  the text of its system (`TestFilesExtractTarEntryCollisionTexts`).
- Linux, macOS and Windows: the unsafe names and the names that stay inside.
  The four Windows names `NUL`, `a:b`, `..\evil2` and `x\y.txt` run on Windows
  (`TestFilesExtractTarUpThenBackIn`).
- Linux and macOS: the mode rows of two file entries with one name
  (`TestFilesExtractTarLastEntryGivesTheMode`).
- Linux, macOS and Windows: the content rows of two file entries with one name,
  and on Windows the name `A.TXT`
  (`TestFilesExtractTarSecondFileEntryReplaces`).
- Linux, macOS and Windows: the two failing directory entry rows
  (`TestFilesExtractTarDirectoryEntryTexts`).
- Linux, macOS and Windows: the marker rows of the archive
  (`TestFilesExtractTarMarkerReplacesArchiveEntry`), and on Linux and macOS the
  mode of the marker (`TestFilesExtractTarMarkerModeIsFixed`).
- Linux and macOS: a link named `.synced` that leads out of `destDir` goes as a
  link, and its target stays (`TestSyncedMarkerRemoveStaysInDestDir`).

- Linux and macOS: the two rows of the umask 0400 table, with the disk of the
  second row (`TestFilesExtractTarDestDirNotReadable`).
- Linux and macOS: the rows of the parent modes `0300`, `0100` and `0000`
  (`TestFilesExtractTarParentCannotBeOpened`).
- Linux and macOS: the rows of a folder above `destDir` that cannot be made
  (`TestFilesExtractTarParentCannotBeMade`). The prefix of the two rows with a
  file at the parent is held on each system (`TestFilesExtractTarParentIsAFile`).
- Linux and macOS: three rows of a wipe that fails, with what stays
  (`TestFilesExtractTarWipeFailure`), and the row of a parent of mode `0500`
  with no `destDir` (`TestFilesExtractTarMkdirFailure`).
- Linux and macOS: the three rows of a parent that cannot be searched
  (`TestFilesExtractTarParentCannotBeSearched`).
- Linux and macOS: the row of a last name of 256 bytes
  (`TestFilesExtractTarLastNameTooLong`). The row is from a Linux VM. On macOS
  the test holds the answer of claustrum, not a measured row.
- Linux and macOS: the mode `0700` of a new `destDir` and of the new folders
  above it (`TestFilesExtractTarNewFoldersHaveMode0700`).
- Linux and macOS: links in the old content and a `destDir` that is a link
  leave the folder outside as it is (`TestFilesExtractTarWipeStaysInDestDir`).
- Windows: the last names `dest.`, `dest` with a space and `dest\.`
  (`TestFilesExtractTarWindowsSpellingsOfDestDir`), and a junction in the old
  content (`TestFilesExtractTarWipeLeavesJunctionTarget`).

These tests hold rules of claustrum that are its own:

- Linux and macOS: a `destDir` that another process replaces with a link
  between the mkdir and the open fails the request. For a link that leaves the
  parent folder the text is `open destDir: openat dest: path escapes from
  parent`. For a link that stays in the parent folder it is
  `open destDir: "<name>" changed while it was opened`. Nothing is written or
  removed behind the link (`TestFilesExtractTarRefusesSwappedDestDir`).
- Windows: the same with a junction, which gets the first of the two texts
  (`TestFilesExtractTarRefusesJunctionSwappedDestDir`).
- Linux and macOS: a marker that the daemon cannot create answers a text with
  the name `.synced` and no whole path
  (`TestSyncedMarkerWriteGoesThroughTheHandle`).
- The test of the two views, and the home guard by identity: see below.

From the code: claustrum opens the parent folder of `destDir` as a handle. It
removes the last name of `destDir` through that handle as a tree, makes the new
folder there, and opens it there. The texts `RemoveAll dest` and `mkdirat dest`
are those of the Go library for such a handle. The wipe cannot take the parent
folder by the names `.` and `..`, because the handle refuses them
(`TestWipeDestDirCannotTakeTheParent`). On Windows the handle refuses a last
name of dots and spaces alone too
(`TestFilesExtractTarLastNameOfDotsAndSpaces`, with the rows of the section
"Last names that get a `RemoveAll <name>` refusal"). A `destDir` that the home guard refuses
reaches none of these steps
(`TestFilesExtractTarRefusedHomeCreatesAndDeletesNothing`).

Before the wipe claustrum compares the path of `destDir` as the system resolves
it with its last name inside the handle. If the two do not name one entry, it
answers `clean destDir: "<name>" is not the entry that the path names`, and it
deletes nothing and makes no `destDir`. The folders above `destDir` from the
first step stay. The same test follows the mkdir, with the prefix
`mkdir destDir:`, and the entry at that name goes with a plain remove
(`TestFilesExtractTarWipeNeedsOneEntryInBothViews`, with a seam). On Windows a
path with the `\\?\` prefix and a dot or a space after its last name gives these
answers (`TestFilesExtractTarWipeLeavesSiblingOfPrefixedName`). This rule is
claustrum's own. A name that the handle refuses gets the text of a remove
through the handle, and no remove runs. Those are the Windows last names of
the section above.

The entry at the last name also gets the home guard by identity, before the
wipe and before the plain remove. A real folder there that is the home folder,
or a parent folder of it, gets the home refusal of the method. The archive is
gone then, because the refusal comes after its open. On Linux and macOS that
covers a path through a link above the last name
(`TestFilesExtractTarRefusesHomeByIdentity`).

On Windows seven spellings of a temp home folder get the home refusal with
`fileCount` 0 (`TestFilesExtractTarRefusesWindowsSpellingsOfHome`, Windows VM):

| spelling | the guard that answers | the archive |
|---|---|---|
| a dot after home, a space after home, a dot after the folder above home | the text compare | stays |
| `\\?\` before home, the same with a dot or a space after home, the short 8.3 name of home | the test by identity | gone |

The text compare refuses a dot or a space after home because the path is
resolved first. It answers true for `<home>.` and for `<home>` with a space,
and false for `\\?\<home>` and for `\\?\<home>.`
(`TestWipesHomeDirWindowsSpellings`, Windows VM). These rows are runs of the
test binary with a temp home and a stubbed wipe. No daemon cell measured them,
and no reference build is measured with these spellings of the home folder.

claustrum creates the directories and the files of the entries
through a handle of `destDir`, and it creates each file exclusively. The texts
`mkdirat a`, `openat d` and `statat .` are those of the Go library for such a
handle. For a second file entry with one name it removes the first file and
creates a new one. Before the marker it removes the name `.synced` as a tree,
and it creates the marker exclusively. Both steps go through that handle.

A build with the steps of "The preparation of destDir" ran beside `5fd08069`
on Linux, macOS and Windows VMs, without the test of the two views. Each cell
was equal in the reply and on disk: 153 cells on Linux, 111 on macOS and 138 on
Windows. A build with the test of the two views ran on a Windows VM. Its
answers for the `\\?\` rows and for the last names that the handle refuses are
in the sections above. This change with the home guard by identity and the
`open parent:` text for a parent that cannot be searched ran beside `5fd08069`
on a Linux VM in 34 cells. Each cell was equal in the reply and on disk.

The Linux cells with umask 077 and 000 of the first mode table did not run with
this change.

The tests of the method ran on VMs with no failure. On the build without the
test of the two views they ran 20 times each: 45 tests and 900 runs on a macOS
VM, 36 tests and 720 runs on a Windows VM. On a build with the home guard by
identity, a macOS VM ran 36 tests of the method in 180 runs, and 65 tests of a
wider pattern in 195 runs. A Windows VM ran 24 tests in 120 runs and 35 tests
in 105 runs on that build (410 and 306 with subtests), without the tests with
`Home` in the name. A Windows VM ran those apart, two times with 8 tests in 40
runs each (190 with subtests) and no failure:
`TestFilesExtractTarRefusedHomeCreatesAndDeletesNothing`,
`TestFilesExtractTarRefusesWindowsSpellingsOfHome` under its earlier name, with
the four rows that it had then,
`TestFilesExtractTarRefusesHomeDir`, the four `TestWipesHomeDir` tests and
`TestWorktreeRemoveRefusesHomeDir`. `TestFilesExtractTarRefusesHomeByIdentity`
and `TestSyncedMarkerRemoveStaysInDestDir` are for Linux and macOS, and they
did not run on Windows. On the change with the `open parent:` text for a parent
that cannot be searched, a Linux VM ran 36 tests of the method in 180 runs,
with no failure. On a later build of this change a Windows VM ran 45 tests of
the wider pattern in 135 runs (447 with subtests), with no failure. Those
include `TestFilesExtractTarLastNameOfDotsAndSpaces`,
`TestWipesHomeDirWindowsSpellings` and the seven rows of
`TestFilesExtractTarRefusesWindowsSpellingsOfHome`, each 3 of 3. That run was
before the test held the archive state and the four answers. The daemon of that
build equals the build before it in 25 Windows cells. No VM has run
`TestFilesExtractTarLastNameTooLong`.

## Not measured

- A marker that the daemon cannot write, and an entry at `.synced` that it
  cannot remove. claustrum answers `write .synced:` and the count of the files
  written.
- A `destDir` that another process replaces while the request runs, on a
  reference build.
- Two `files.extract_tar` requests at one time on one `destDir`.
- A file system that refuses a mode.
- A link or a FIFO that another process puts at the name of an entry.
- A Windows version before Windows 11, for a reserved name with an extension
  such as `CON.txt`.
- A `destDir` that the daemon cannot open, on Windows.
- A last name of more than 255 bytes on macOS and Windows, and a whole path
  over the path limit.
- `89cb6289` with a `destDir` that is a link and has a separator after its
  name, on macOS.
- The home refusals of the test by identity, on a running daemon. No VM cell
  has a home folder as the target. The tests of claustrum hold these refusals.
- The texts `open parent:` and `mkdir destDir: mkdirat` on Windows. For a
  permission error at the lstat of the last name, claustrum answers
  `clean destDir: RemoveAll <name>: <reason>` on Windows (from the code).
- The code after the `open parent:` fix, on macOS and Windows. The rows of
  claustrum there are from builds before it.
- A `destDir` that names the home folder by a spelling that the text compare
  does not see, on a reference build.
- A Windows access list that denies the delete of an old entry. The daemons ran
  with an administrator token, and the deny entry did not hold for it.
- A folder above `destDir` that cannot be made by a permission, on Windows.
- On macOS and Windows, the two builds with a parent of each mode other than in
  the table. The parent mode rows are from Linux and macOS VMs.
- The modes on Windows for an archive mode that the Windows table does not
  name.
- The umask rows on Windows. Windows has no umask.
