# files.read: measurement record

This page is a record, not reading material. It holds the measurements behind the rules of `files.read`. A new measurement of the method goes into this page. The code comments name the reference build `5db5e4a` for the `maxBytes` measurements. The tests in `integration_fifo_unix_test.go` and `filesread_windows_test.go` pin some rows of the tables, and each file says which.

To use the method, read [files.read](../protocol/files-read.md).

`{path[,maxBytes]}` → `{"content":"<raw text>","exists":true}`
- `content` is raw text, not base64.
- Missing file → `{content:"",exists:false}`, which is not an error.
- A directory → `-32602 files.read: path is a directory`.
- Size > `maxBytes` → `-32602 files.read: file exceeds maxBytes`.
- An absent, `0`, or negative `maxBytes` sets the cap to `262144` (256 KiB). The
  cap is not "unlimited". A file of 262144 bytes reads, and a file of 262145 bytes
  errors. `89cb6289` and `5fd08069` answer the same for both sizes (Linux, macOS
  and Windows VMs). The daemon honors a positive `maxBytes` verbatim, above or
  below the default. The cap uses the size of the open file. That size is `0`
  for the null device and for a file under `/proc`.

## Files that are not regular files

The rule is on the page of the method. This section holds the rows.

`5fd08069` was measured on a Linux VM, a macOS VM and a Windows VM, beside
`89cb6289`. The two builds differ in the rows that the last column marks. In
those rows claustrum before this rule answered as `89cb6289`.

NRF is `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"files.read: not a regular file"}}`.
EMPTY is `{"jsonrpc":"2.0","id":1,"result":{"content":"","exists":true}}`.
ABSENT is `{"jsonrpc":"2.0","id":1,"result":{"content":"","exists":false}}`.
"mb16" is a request with `maxBytes` 16.

### Linux and macOS

The row numbers are those of the second round. "first round" names the round
before it. "validation round" names the round that ran claustrum beside
`5fd08069`. "open round" names the last round, with its cell numbers. On both VMs fd 0 of the daemon was `/dev/null`. On the Linux VM the
daemon had no terminal.

| path | `5fd08069` | Linux VM row | macOS VM row | `89cb6289` |
|---|---|---|---|---|
| a regular file | the content | 1 | 1 | same |
| a symlink to a regular file | the content | first round | not measured | same |
| a directory | `-32602 files.read: path is a directory` | first round | first round | same |
| a symlink to a directory | `-32602 files.read: path is a directory` | 17 | 14a | same |
| a missing path | ABSENT | first round | first round | same |
| a symlink with a missing target | ABSENT | 17 | 14b | same |
| the empty path | ABSENT | 21 | 17a | same |
| `/dev/null` | EMPTY | 2 | 2 | same |
| `/dev/../dev/null` | EMPTY | 13 | 11 | same |
| `/proc/self/root/dev/null` | EMPTY | 13 | not measured | same |
| a symlink to `/dev/null` | EMPTY | 11 | first round | same |
| `/dev/stdin` | EMPTY | 10 | 8a | same |
| `/proc/self/fd/0` on Linux, `/dev/fd/0` on macOS | EMPTY | 10 | 8b | same |
| a second device node with the numbers of `/dev/null`, mode 666 | NRF | 12 | 10a | EMPTY |
| `/dev/zero`, mb16 | NRF in under 3 ms | 3 | 3 | no reply. The daemon grew past 1 GiB and the run ended it |
| `/dev/urandom`, mb16 | NRF | 4 | 4 | as `/dev/zero` |
| `/dev/random`, mb16 | NRF | 5 | 5 | as `/dev/zero` |
| `/dev/full`, mb16 | NRF | 6 | not measured | as `/dev/zero` |
| a symlink to `/dev/zero`, mb16 | NRF | 11 | 9a | as `/dev/zero` |
| a second device node with the numbers of `/dev/zero`, mb16 | NRF | 12 | 10b | as `/dev/zero` |
| `/dev/ptmx` | NRF | 8 | 7 | no reply in 5 s on Linux and in 8 s on macOS. The daemon lived |
| a FIFO with no writer | NRF in under 3 ms | first round | first round | no reply in 5 s. EMPTY after a writer opened and closed |
| a FIFO, a writer waits in its open | NRF. The open of the writer returned at the request | first round, 1 run, and validation round, 11 runs | first round and validation round, 4 runs each | `{"content":"hi","exists":true}` |
| a symlink to a FIFO with no writer | NRF | 11 | 9b | no reply in 5 s |
| a block device that the daemon user can read, mb16 and no `maxBytes` | NRF | 14 (a loop device) | 12a, 12b, 13a (a RAM disk) | the whole 1048576 bytes, also with mb16 |
| the raw node of that RAM disk, mb16 and no `maxBytes` | NRF | no such node | 12c, 12d, 13b | `-32603 read /dev/rdisk5: invalid argument` |
| a block device that the daemon user cannot open, and its raw node on macOS | `-32603 open <path>: permission denied` | 15 | 13c, 13d | same |
| a character device of mode 0600 that root owns | `-32603 open <path>: permission denied` | 16 | not measured | same |
| a regular file of mode 0000 | `-32603 open <path>: permission denied` | 18 | 15a | same |
| a FIFO of mode 0000 | `-32603 open <path>: permission denied` | 18 | 15b | same |
| `/dev/tty` | `-32603 open /dev/tty: no such device or address` on Linux, `-32603 open /dev/tty: device not configured` on macOS | 7 | 6 | same |
| `/dev/kmsg`, with `kernel.dmesg_restrict` 1 | `-32603 open /dev/kmsg: operation not permitted` | 9 | no such node | same |
| an `AF_UNIX` socket, bound or listening | `-32603 open <path>: no such device or address` on Linux, `-32603 open <path>: operation not supported on socket` on macOS | 19 and first round | 16 and first round | same |
| a symlink loop | `-32603 open <path>: too many levels of symbolic links` | 17 | 14c | `-32603 stat <path>: too many levels of symbolic links` |
| a regular file with a `/` after its name | `-32603 open <path>/: not a directory` | 21 | 17b | `-32603 stat <path>/: not a directory` |
| a directory of mode 0000, and one of mode 0333 | `-32603 open <path>: permission denied` | open round 1, 2 | open round 1, 2 | `-32602 files.read: path is a directory` |
| a directory in a parent of mode 0000 | `-32603 open <path>: permission denied` | open round 3 | open round 3 | `-32603 stat <path>: permission denied` |
| a regular file of mode 0000, 4 bytes, `maxBytes` 1 | `-32603 open <path>: permission denied` | open round 4 | open round 4a | `-32602 files.read: file exceeds maxBytes` |
| a regular file of mode 0000, 262145 bytes, no `maxBytes` | `-32603 open <path>: permission denied` | open round 5 | open round 5 | `-32602 files.read: file exceeds maxBytes` |
| `a.txt/../a.txt`, where `a.txt` is a regular file | `-32603 open <path>: not a directory` | open round 6 | open round 6a | `-32603 stat <path>: not a directory` |
| a last name of 300 characters | `-32603 open <path>: file name too long` | open round 6 | open round 6b | `-32603 stat <path>: file name too long` |
| a NUL byte in the path | `-32603 open <path>: invalid argument` | open round 6 | open round 6c | `-32603 stat <path>: invalid argument` |
| `a.txt/b`, where `a.txt` is a regular file | `-32603 open <path>: not a directory` | open round 7 | open round 7 | `-32603 stat <path>: not a directory` |
| `/proc/self/status` | the content, 61 lines | 20 | no such file | same |
| `/proc/self/mem` | `-32603 read /proc/self/mem: input/output error` | 20 | no such file | same |
| a file under `/sys` | the content | 20 | no such file | same |

More facts of these rows:

- In the endless device rows `5fd08069` stayed under 9 MiB and answered
  `server.ping` after the refusal. `maxBytes` 16 did not bound the read of
  `89cb6289`.
- After the refusal of a FIFO with no writer, a writer open without a wait
  failed with "no such device or address" (Linux VM). So `5fd08069` held the
  FIFO open no more.
- In the row with a waiting writer, the reply of `5fd08069` was NRF in each run.
  On the macOS VM the write of that writer failed with "broken pipe" in 7 of 8
  runs and wrote 2 bytes in 1. On the Linux VM it failed in 10 of 12 runs and
  wrote 2 bytes in 2.
- The null device rows and the second node row show that the test is the file
  itself. The device numbers of the two nodes are equal.

### Windows

The daemons ran with an administrator token at the high integrity level. Two passes gave the same lines.

| path | `5fd08069` | Windows VM row | `89cb6289` |
|---|---|---|---|
| a regular file | the content | 1 | same |
| `NUL`, `nul`, `NUL:`, `\\.\NUL`, `\\?\NUL`, `<folder>\NUL` | NRF | 2, 5a, 5c, 5d, 5e, 3 | EMPTY |
| `<missing folder>\NUL`, `NUL.txt`, `PRN`, `LPT1` | ABSENT | 4, 5b, 6e, 6g | same |
| `CON`, `CONIN$`, `CONOUT$` | `-32603 open CON: The handle is invalid.`, each with its own name | 6a, 6b, 6c | `-32603 CreateFile CON: The handle is invalid.` |
| `COM1`, `AUX` | NRF in 78 to 90 ms | 6d, 6f | no reply in 5 s |
| a named pipe with no data, 16 instances | NRF | 7a | no reply in 5 s |
| a named pipe that writes `hi`, 16 instances | NRF | 7b | `{"content":"hi","exists":true}` |
| a named pipe with 1 instance, with and without data | NRF | 7c, 7d | `-32603 open \\.\pipe\<name>: All pipe instances are busy.` |
| `\\.\PhysicalDrive0` | `-32603 GetFileInformationByHandle \\.\PhysicalDrive0: Incorrect function.` | 8a | same |
| `\\.\C:` | `-32603 GetFileInformationByHandle \\.\C:: The parameter is incorrect.` | 8b | `-32603 GetFileInformationByHandle \\.\C:: Incorrect function.` |
| a folder, a folder with a `\` after its name | `-32602 files.read: path is a directory` | first round, 10a | same |
| a junction to a folder, a symbolic link to a folder | `-32602 files.read: path is a directory` | 9a, 9b | same |
| a symbolic link to a regular file | the content of the target | 9c | same |
| a missing file, a symbolic link with a missing target, the empty path | ABSENT | first round, 9d, 10c | same |
| a regular file with a `\` after its name | `-32603 open <path>\: The directory name is invalid.` | 10b | `-32603 CreateFile <path>\: The directory name is invalid.` |
| a file that another process holds with share mode 0 and read access | the content | 11a | same |
| the same file, the holder has read and write access | `-32603 open <path>: The process cannot access the file because it is being used by another process.` | 11b | same |
| a file of 4 bytes that another process holds with no sharing, `maxBytes` 1 | `-32603 open <path>: The process cannot access the file because it is being used by another process.` | open round 1a | `-32602 files.read: file exceeds maxBytes` |
| the same for a file of 262145 bytes, no `maxBytes` | the same `open` text | open round 2a | `-32602 files.read: file exceeds maxBytes` |
| a NUL byte in the path | `-32603 open <path>: invalid argument` | open round 5a | `-32603 Stat <path>: invalid argument` |
| a last name of 300 characters | `-32603 open <path>: The filename, directory name, or volume label syntax is incorrect.` | open round 5b | `-32603 CreateFile <path>: …` with the same reason |
| `reg.txt\b`, where `reg.txt` is a regular file | ABSENT | open round 6 | same |
| `reg.txt` with a space or a dot after its name | the content of `reg.txt` | open round 7a, 7b | same |
| the stream `reg.txt:s` | the content of the stream | 12a | same |
| `reg.txt::$DATA` | the content of the file | 12b | same |

In rows 7a and 7b the pipe saw 1 client of `5fd08069` and 2 clients of
`89cb6289`.

### claustrum

The tests of claustrum pin these rows:

- Linux and macOS: a FIFO with no writer, a FIFO with a waiting writer, a symlink
  to a FIFO, `/dev/null`, a symlink to `/dev/null`, a symlink loop and a regular
  file with a `/` after its name.
- Linux and macOS: the null device test is the identity of the file. The test
  uses two FIFOs and a seam, because a device node needs root.
- Linux and macOS: a directory of mode 0000, and a file of mode 0000 over
  `maxBytes`.
- Linux: a bound `AF_UNIX` socket.
- Windows: `NUL` in the six spellings of the table, and a named pipe.

A build of claustrum with this rule ran beside `5fd08069` on Linux, macOS and
Windows VMs, in the validation round and in the open round. Each `files.read`
reply line was byte-equal, except the content of `/proc/self/status`. That
content holds the pid and the memory numbers of the daemon.

### Not measured

- A kind of character device, block device or pipe that the tables do not name.
- A FIFO on Windows.
- A Windows daemon with no administrator token, on `\\.\PhysicalDrive0` and on `\\.\C:`.
- `/dev/stdin` with an fd 0 that is not `/dev/null`.
- How `5fd08069` keeps its open from a wait for a FIFO writer. claustrum opens
  with `O_NONBLOCK` on Linux and macOS (from the code).
- A regular file under a lease. With `O_NONBLOCK` the open of claustrum gets an
  error there, where a plain open waits (from the code).
- A second node of the null device inside the file system of `/dev`. Both
  measured second nodes were on another file system.
- On Windows, a folder or a file whose access list denies read data. The cells
  ran, but the deny entry did not stop the daemons with their administrator
  token.
