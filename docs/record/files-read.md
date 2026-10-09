# files.read: measurement record

This page is a record, not reading material. Its text moved here from the protocol reference on 2026-10-09. Only one cross-reference changed in the move. It holds the rules of `files.read` and the measurements behind them. A new measurement of the method goes into this page. Tests of the repository pin the FIFO row with a writer and the `/dev/null` row in both columns. They also pin the FIFO row with no writer in the column of the flag, and the socket row at the default on Linux.

To use the method, read [files.read](../protocol/files-read.md).

`{path[,maxBytes]}` → `{"content":"<raw text>","exists":true}`
- `content` is raw text, not base64.
- Missing file → `{content:"",exists:false}`, which is not an error.
- A directory → `-32602 files.read: path is a directory`.
- Size > `maxBytes` → `-32602 files.read: file exceeds maxBytes`.
- An absent, `0`, or negative `maxBytes` sets the cap to `262144` (256 KiB). The
  cap is not "unlimited". A file of 262144 bytes reads, and a file of 262145 bytes
  errors. The daemon honors a positive `maxBytes` verbatim, above or below the
  default. The cap uses the stat size. On linux that size is `0` for every
  non-regular kind, so the cap never bounds a FIFO, socket or device on either
  binary.
- Non-regular files have an opt-in guard, D4. It is off by default, which is
  parity. The reference reads `/dev/null` as `{"content":"","exists":true}` and
  blocks on a writerless FIFO, and it refuses neither. Set
  `-files-read-regular-only`, or the `files-read-regular-only` configuration key.
  Every non-regular path then answers `-32602 files.read: not a regular file`,
  which is a frame the reference never produces. The predicate is
  `Mode().IsRegular()`. It is whole and not narrowable, because `/dev/null` and
  `/dev/zero` are indistinguishable by mode. The full measurement and the reason
  are in [`DIVERGENCES.md`](../DIVERGENCES.md) → D4.

  | path | reference and claustrum at the default | with `-files-read-regular-only` |
  |---|---|---|
  | **CONTROL** a regular file | `{"content":"…","exists":true}` | *(unchanged, the guard does not apply)* |
  | **CONTROL** a regular file over `maxBytes` | `-32602 files.read: file exceeds maxBytes` | *(unchanged)* |
  | a FIFO, writer paired | `{"content":"<bytes written>","exists":true}` | `-32602 files.read: not a regular file` |
  | a FIFO, no writer | no frame until a writer opens | `-32602 files.read: not a regular file` |
  | `/dev/null` | `{"content":"","exists":true}` | `-32602 files.read: not a regular file` |
  | a bound `AF_UNIX` socket | `-32603 open <p>: no such device or address` on linux. *darwin/amd64 says `operation not supported on socket`, a per-OS stdlib difference that is identical between binaries on each OS* | `-32602 files.read: not a regular file` |
  | an unreadable character device (`/dev/console`) | `-32603 open <p>: permission denied` | `-32602 files.read: not a regular file` |
  | an unreadable block device (`/dev/nvme0n1`) | `-32603 open <p>: permission denied` | `-32602 files.read: not a regular file` |

  The two device rows assume a non-root daemon, because they are permission
  failures. The opted-in column is measured for the FIFO and `/dev/null` rows.
  For the socket row and the two device rows it is entailed by a false
  `Mode().IsRegular()`, and was not run separately.
  The default gives up two things. A writerless FIFO parks a request goroutine and
  a descriptor until a writer arrives. An unbounded device read (`/dev/zero`) grows
  the daemon until the kernel OOM-kills it. Both are the reference's own behaviour,
  and both are measured. The forensics are condensed out of the committed docs.
