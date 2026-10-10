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

## A destDir that the daemon cannot open

With a daemon umask of 0400 the new `destDir` has mode `0300`. `89cb6289`
extracts the archive there, and claustrum before this change did the same (from
the code). `5fd08069` fails, and claustrum fails with the same text.

| case | system | `5fd08069` | `89cb6289` | claustrum |
|---|---|---|---|---|
| `destDir` does not exist | Linux and macOS VMs | `open destDir: openat dest: permission denied`. `destDir` is empty afterwards | success. Each new file has mode `0200` | the text of `5fd08069`. `destDir` is empty afterwards |
| two folders above `destDir` do not exist either | Linux and macOS VMs | `open parent: open <parent>: permission denied`. No `destDir` afterwards | success | the text of `5fd08069`. An empty `destDir` afterwards |

The claustrum column is from Linux and macOS VMs. In the second
row the text is equal and the disk is not: claustrum creates `destDir` before
it opens the parent, and `5fd08069` leaves no `destDir`. On the VMs an earlier
build of this change opened `destDir` by its whole path. It answered
`open destDir: open <destDir>: permission denied` in both rows.

## Texts that claustrum keeps

In these rows `5fd08069` has a text that claustrum does not have. claustrum
answers as `89cb6289` does, byte for byte. Each row is from the VMs that the
system column names, for the two builds and for claustrum.

| case | system | `5fd08069` | `89cb6289` and claustrum |
|---|---|---|---|
| the parent of `destDir` has mode `0300`, and `destDir` has old content | Linux and macOS VMs | `open parent: open <parent>: permission denied` | `clean destDir: open <parent>: permission denied` |
| an old `destDir` of mode `0300` | Linux and macOS VMs | `clean destDir: RemoveAll dest: permission denied` | `clean destDir: openfdat <destDir>: permission denied` |
| an old `destDir` of mode `0500` with content | Linux and macOS VMs | `clean destDir: RemoveAll dest: permission denied` | `clean destDir: unlinkat <destDir>/old.txt: permission denied` |
| another process holds a file of the old `destDir` open | Windows VM | `clean destDir: RemoveAll dest: The process cannot access the file because it is being used by another process.` | `clean destDir: unlinkat <destDir>\a.txt:` and the same reason |
| the parent of `destDir` has mode `0500`, and `destDir` does not exist | Linux and macOS VMs | `mkdir destDir: mkdirat dest: permission denied` | `mkdir destDir: mkdir <destDir>: permission denied` |

The old content stays in each `clean destDir:` row of the Linux and macOS VMs.
In the Windows row the held file stays and the other old entries are gone. The
disk is equal on the two builds and on claustrum in each row.

With a parent folder of mode `0500` and an old `destDir` with content, the old
content is gone on `5fd08069` and on claustrum, and both answer a
`clean destDir:` failure (Linux VM). `89cb6289` is not measured there.

## Spellings of destDir

Each row is a success on the builds that the last column names:

| `destDir` | system | builds |
|---|---|---|
| a separator after the last name | Windows VM | `89cb6289` and `5fd08069` |
| a separator after the last name, `/./` inside the path, a parent folder that is a symbolic link | Linux VM | `5fd08069` |
| a path through the `/tmp` link | macOS VM | `89cb6289` and `5fd08069` |
| forward slashes, another letter case, a short name, and the `\\?\` prefix | Windows VM | `89cb6289` and `5fd08069` |
| `destDir` is a symbolic link to a folder | Linux, macOS and Windows VMs | `89cb6289` and `5fd08069` |
| `destDir` is a junction to a folder | Windows VM | `89cb6289` and `5fd08069` |

In the two last rows `destDir` is a real folder afterwards, and the target of
the link keeps its content. With another letter case or a short name, the new
folder has the name as the request spelled it (Windows VM).

Old content that is read-only is replaced on both builds (Windows VM): a
read-only file at the name of an entry, a read-only folder, and a read-only
folder at the name of a file entry.

More spellings are a success on `5fd08069` and on claustrum, with the files in
the same place: a `..` inside the path and a doubled separator (Linux VM), and
a `..` inside the path and a `destDir` two new folders deep (Windows VM). A
`destDir` with the last name `.synced` gets the marker file inside it (Linux
and Windows VMs).

One spelling differs on disk. On the Windows VM a `destDir` whose last name
ends in a dot or a space (`dest.`, `dest `) names the folder `dest`. `5fd08069`
removes the old content of that folder. claustrum keeps the old file beside the
new ones, and the reply is a success on both. `89cb6289` is not measured there.
The step that removes the old content is the same as before this change (from
the code).

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

Three tests hold rules of claustrum that no reference row covers:

- Linux and macOS: a `destDir` that another process replaces with a link
  between the mkdir and the open fails the request. For a link that leaves the
  parent folder the text is `open destDir: openat dest: path escapes from
  parent`. For a link that stays in the parent folder it is
  `open destDir: "<name>" changed while it was opened`. Nothing is written or
  removed behind the link (`TestFilesExtractTarRefusesSwappedDestDir`).
- Windows: the same with a junction, which gets the first of the two texts
  (`TestFilesExtractTarRefusesJunctionSwappedDestDir`). No VM has run this
  test.
- Linux and macOS: the two rows of the umask 0400 section
  (`TestFilesExtractTarDestDirNotReadable`).
- Linux and macOS: a marker that the daemon cannot create answers a text with
  the name `.synced` and no whole path
  (`TestSyncedMarkerWriteGoesThroughTheHandle`).

From the code: claustrum creates the directories and the files of the entries
through a handle of `destDir`, and it creates each file exclusively. The texts
`mkdirat a`, `openat d` and `statat .` are those of the Go library for such a
handle. For a second file entry with one name it removes the first file and
creates a new one. Before the marker it removes the name `.synced` as a tree,
and it creates the marker exclusively. Both steps go through that handle.

This change ran beside `5fd08069` on Linux, macOS and Windows VMs. Each cell
that ran with this change was equal in the reply and on disk, apart from the
cells below:

- Linux VM: 105 cells. The 5 rows of "Texts that claustrum keeps" and of the
  second row of "A destDir that the daemon cannot open" differ as those
  sections say. 3 more cells differ in the text alone: the parent of `destDir`
  has mode `0100` or `0500`. `5fd08069` answers `open parent: …` or
  `clean destDir: RemoveAll dest: …` there, and claustrum answers its
  `mkdir destDir:` or `clean destDir:` text. The disk is equal.
- macOS VM: 62 cells, with the same 5 rows as the only differences.
- Windows VM: 96 cells. One differs in the text: the row of the held file. Two
  differ on disk: the `destDir` names that end in a dot or a space, in
  "Spellings of destDir".

The Linux cells with umask 077 and 000 of the first mode table did not run with
this change.

The tests of the method ran 20 times each with no failure, 660 runs on a macOS
VM and 1840 runs on a Windows VM. `TestSyncedMarkerRemoveStaysInDestDir` is for
Linux and macOS, and it did not run on Windows.

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
- The modes on Windows for an archive mode that the Windows table does not
  name.
- The umask rows on Windows. Windows has no umask.
