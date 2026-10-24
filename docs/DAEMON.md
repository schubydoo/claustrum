# Running the daemon

This page holds what a `-serve` daemon does off the JSON-RPC wire. The flags
are under [Daemon lifecycle (flags)](PROTOCOL.md#daemon-lifecycle-flags).

A rule with no mark matches the reference as measured on `5fd08069` or on an
earlier build. A build that a section names in its first lines holds for that
section. The other marks are those of the [protocol reference](PROTOCOL.md).

## Token persistence (`daemon.token`)

The file sits beside the socket, off the JSON-RPC wire. With it a client can
reconnect and authenticate after the `-token-file` was unlinked, or after the
`-token-fd` pipe closed.

Once the socket is listenable, the daemon writes the token that it holds in memory to
`daemon.token` in the directory of the socket, with mode `0600`. The write is
atomic, through a `daemon.token-*` temp file and a rename. A failed write logs
`[daemon] failed to persist token: …`, and the daemon serves on. A new daemon
replaces the token of a dead daemon only after its reap, its `bind` and its
`listen`, and until then the file holds the old token (measured on Linux).

The daemon unlinks the file only on a graceful stop: `server.shutdown`,
`SIGTERM`, `SIGINT` or its self-exit. A `SIGKILL` or a crash leaves it. `-stop`
also removes it after a failed connect, unless it prints `survivor` (see
[-stop](PROTOCOL.md#-stop-ask-a-running-daemon-to-shut-down)). The unlink tests identity (`os.SameFile`). If the file
at the path is not the file that the daemon wrote, the file stays. So a
departing daemon keeps the token of a successor (measured on Windows, from the
code on Linux and macOS). The same guard covers `rpc.sock` on Linux and macOS and
the `rpc.pipe` pointer on Windows. A token or pipe pointer with no recorded
identity stays. A socket with no recorded identity goes with a plain unlink
(both claustrum's own).

Two states remove the socket of a successor, as on the reference:

- On Linux ext4 the new socket can get the inode of the old one. The old daemon
  then removes it as its own (measured on Linux).
- On Windows, after a take-over, the stop of the old daemon removes it
  (measured on Windows).

The launcher half of the handoff is under [Daemon startup](#daemon-startup-serve).

The fixed name and the folder of the socket are the reconnect contract, so they
are not configurable. On Windows `0600` is not an owner-only DACL, a limit of
`os.CreateTemp` in Go. The per-user session folder is the confinement, and
claustrum does not "fix" this parity caveat. On Linux and macOS the
[run-dir lock](#run-dir-lock-daemonlock) evicts a prior live daemon of the same
socket before the new one binds. Two daemons still collide on this file where an
eviction is refused, and on Windows, which has no run-dir lock.

## Run-dir lock (`daemon.lock`)

The lock exists on Linux and macOS only. It is a file beside the socket, off the
JSON-RPC wire. A rule of this section without a mark is measured on Linux. A rule
marked "also macOS" is measured on macOS too.

Before it binds the socket, a `-serve` daemon takes an exclusive `flock` on
`daemon.lock` in the directory of the socket (mode `0600`). It writes an owner
record into the file, a JSON object. Both of its two forms are measured on macOS
too. Until the reap of its start ends, the record is
`{"pid":<n>,"role":"serve","node":"<node>"}`. After the reap and the bind, it is
`{pid, role, node, instanceId, startedAt}`, with the `instanceId` of the
listening line and the `instanceId` and `startedAt` that `server.capabilities`
answers. The `startedAt` match is measured on macOS too. From the code:
`node` is the boot id joined to the pid-namespace inode on Linux, and
`sysctl kern.bootsessionuuid` on macOS, and the record has no `node` if that
identity is unknown.

On a graceful stop the daemon removes its own `rpc.sock`, kills its child groups
and then removes `daemon.token` (see
[What the stop does](protocol/server.md#what-the-stop-does)). Last it truncates
the record and releases the lock. `daemon.lock` stays, empty.

### Eviction of a live holder

A rule here without a mark is measured on Linux.

A prior live daemon of the same socket can hold the lock. The new daemon then
sends it `SIGTERM` and tries the lock every 50 ms for 2 s. If the lock is still
held, it tests the holder again and sends `SIGKILL`. Then it tries the lock for
1 s more (claustrum's own value). It waits on the lock, not on the pid. On Linux
both signals go through one `pidfd_open` descriptor with `pidfd_send_signal`. On
macOS claustrum uses `kill` (the call of the reference there is not measured).

The daemon logs these lines. `<who>` is `pid <n> (instance "<hex>")` for a
holder with the full record, and `pid <n>` for the short record. The first two
lines, in the `pid <n>` form, are also macOS.

```
[daemon] serve: run dir is held by a live daemon, <who>; sending SIGTERM
[daemon] serve: previous daemon <who> exited after SIGTERM
[daemon] serve: WARNING previous daemon <who> ignored SIGTERM for 2s; sending SIGKILL (its Claude Code children, if any, are ended next, before this daemon serves)
[daemon] serve: previous owner of <dir>: killed
```

The summary line `[daemon] serve: previous owner of <dir>: <outcome>` ends in
`terminated` after a `SIGTERM`, in `killed` after a `SIGKILL`, and in `survivor`
for a holder that stays. A holder that the `SIGKILL` stopped gets no "exited"
line.

A signal can reach a daemon during its own start, for example from a second
daemon or from `-stop`. The daemon handles one `SIGTERM` or
`SIGINT`. It logs one line, for example
`[daemon] received terminated; shutting down (children will be killed)`, or
`received interrupt` with the same tail. It finishes the reap of its start. A
daemon in the eviction of an older holder still evicts it. It sends the child of
that holder `SIGTERM` before it binds. Then it binds, listens, writes its token,
shuts down and exits 0. For a `SIGTERM` from a second daemon these rules are also
macOS. A `-stop` during the reap prints `terminated` and exits
0. The launcher of the signalled daemon exits 1 after about 12 s in two cases: a
signal during an eviction, and a `-stop` during the reap. Its text is
`daemon did not take over <socket> (predecessor still owns it)`. A second daemon
that sent `SIGKILL` to a first one during its start takes the lock and serves
with its full record.

### Holders that get no signal

A rule here without a mark is measured on Linux.

The eviction fires only against our own `-serve` daemon for this socket, on this
machine. If the first test of the holder fails, the daemon signals nothing and
serves without the lock. It logs a refusal line and, except for an unusable
record, the summary `survivor`. claustrum refuses a holder on another machine or
container that shares the folder. It also refuses a holder with an unknown
machine identity, and a holder that is not its serve process for this socket.
For these three refusals both texts are claustrum's own, not measured. A holder
that survives `SIGKILL` stays, with the summary `survivor` (from the code, not
measured).

If the second test of the holder fails, right before the `SIGKILL`, the daemon
treats the holder as gone. It logs
`[daemon] serve: pid <n> is no longer our serve holder before SIGKILL (<reason>); treating the previous daemon as gone`
and the summary `terminated`, and tries the lock once more. Normally it then
serves with the lock.

A holder whose record has the role `stop` gets no signal. The daemon tries the
lock every 50 ms for 2 s. Then it logs this line and serves without the lock
(also macOS):

```
[daemon] serve: <dir>/daemon.lock is held by a --stop (pid <n>) that has not let go; leaving it
```

A process that is no daemon can hold the lock, with no owner record. The daemon
signals nothing and logs three lines:

```
[daemon] serve: WARNING <dir>/daemon.lock is held by a live process we will not signal (its holder is not a daemon (role "")); leaving it alone
[daemon] serve: previous owner of <dir>: survivor
[daemon] serve: not the run dir's lock holder; leaving any predecessor's children alone and recording none
```

claustrum logs the third line at every start that ends without the lock
(claustrum's own beyond this case, not measured). Such a daemon reaps no
children and records none. Other role texts are not measured.

### The late lock

A rule here without a mark is measured on Linux.

While another process holds the lock, the daemon tries it again every 30 s
(measured for a holder of an empty file, also macOS). A failed try logs nothing.
A good try writes the full record, holds the lock until the exit and logs this
line (also macOS):

```
[daemon] run-dir lock acquired late (previous holder gone); recorded pid <n> (instance "<hex>")
```

claustrum also tries again in every other case where another process keeps the
lock (claustrum's own). It does not try again after a failed open of the file or
a refused lock call.

### Windows and macOS

Windows has no run-dir lock (measured on Windows). The remove-then-rebind
handoff of the socket keeps two daemons apart there, and the "not the run dir's lock holder"
line above is the first log line of every start (measured on Windows).

On macOS claustrum verifies the holder through `sysctl KERN_PROCARGS2` before it
signals it, in the eviction and in `-stop`. The reference skips that test
(measured on macOS). See
[DIVERGENCES.md](DIVERGENCES.md) D15.

## Host cleaner (off-wire, Linux and macOS)

A periodic sweep ends orphaned Claude Code process groups, retires abandoned
daemons and tidies stale run dirs. It reaches no JSON-RPC frame. A rule with no mark was
measured against `89cb6289` on a Linux VM. A rule that names macOS was measured on
a macOS VM against `89cb6289`. A rule marked `5fd08069` or `f6010b97` was measured
against that build.

claustrum links no cleaner on Windows. The Windows references logged no cleaner
line and changed nothing, in a check with no positive control.

### When the cleaner runs

The cleaner is on in one case only. The socket path is cleaned first, so `.` and
double slashes drop out, and `..` is resolved by text (from the code for `..`). The
cleaned path is absolute, and its last three components read `run/<id>/rpc.sock`,
with that exact case. The folder above `run` is the root. The resolved daemon
binary is `<root>/srv/<id>/<name>`, one folder below `srv`, under that root or
under the symlink-resolved form of that root. The first pass then runs 15 s after
the start. In every other case the cleaner is off and runs no pass. The daemon
then logs one of two lines, right after the listening line:

```
[daemon] host cleaning off: socket path "<socket>" is not <root>/run/<id>/rpc.sock
[daemon] host cleaning off: executable "<binary>" is not a deployed daemon under <root>/srv
```

The socket line names the socket as given. The executable line names the resolved
binary and the `srv` of the root as given. A symlink to `run` with another name
turns the cleaner off with the socket line. macOS gave the same decisions in the
cases that it ran.

A path through a symlink to the root or to a folder above it keeps the cleaner on,
for the socket and for the binary. If the daemon cannot read its own executable
path, the binary test does not run (not measured).

A pass acts on the root as the socket path gives it and on its resolved form, the
same folder under its real path. It never acts on the root of the binary. If the resolution fails, it acts on the root as given
only (not measured). A pass lists each run folder once, and its lines name each
in the spelling of the socket as given.

### Signals of the cleaner

A stranded daemon is a daemon whose socket path no longer leads to it. On Linux
and macOS the cleaner sends no signal to such a daemon or to its children. The
daemon ends by itself, see [Orphan-exit self-probe](#orphan-exit-self-probe).

The cleaner retires an abandoned daemon with `SIGTERM`. That is a daemon on a run
dir that is idle past the threshold, or whose name carries `.removing-` from an
earlier removal. A listener that is not the daemon binary, or that serves another
socket, gets no signal. On macOS the first case is measured, and the second is
not measured. This gate is parity, not [D15](DIVERGENCES.md#d15). A daemon
that is too young is not retired, and the run dir gets this line (measured on
macOS for a daemon about 33 s old). The age of 5 minutes is claustrum's own:

```
[hostclean] run dir "<dir>" unused for <n> days: its daemon (pid <pid>) was not retired: it is too young to judge
```

On Linux the cleaner opens a pid descriptor before its last identity test and
signals through it with `pidfd_send_signal`, as on the reference. So a pid that
changes hands after that test gets no signal (from the code). On a Linux kernel without that call claustrum uses `kill` (not
measured). macOS uses `kill`, and on macOS both cleaners end the same daemons and
groups and remove the same folders. The signal call of the macOS reference is not
measured.

On Linux (`5fd08069`) a stopped daemon on an idle run dir:

- gets no SIGTERM, and the dir stays
- gives the run dir the `was not retired` line with the reason
  `the listener could not be verified as a daemon of ours (not inspectable)`
- gets this line too if it is past claustrum's own age of 5 minutes, and counts
  as undecided:

```
[hostclean] daemon pid <pid> left alone this pass: it could not be inspected (or is stopped)
```

### The `lsof` reads (macOS)

Every rule of this sub-section holds on macOS, unless it names Linux. Linux runs no `lsof`, and it logs
neither `left alone this pass` line below (from the code). The cleaner asks `lsof` three things:

- whether a daemon it is about to retire still has a live client
- whether the stdio of an orphan is three pipes
- whether a process holds the `daemon.lock` of an idle run dir

Each run has a command deadline. The cleaner gives up on a run that outlives a
later bound. Both bounds are claustrum's own. The outcomes of a run:

| `lsof` outcome | busy read of a daemon | lock read of a run dir |
|---|---|---|
| the command does not start | the daemon gets no SIGTERM | a dir with a lock file stays |
| the run writes to stderr and exits 1 | the daemon gets no SIGTERM | a dir with a lock file stays |
| the cleaner gives up on the run | no SIGTERM, the `was not retired` line and no `left alone` line (`5fd08069`) | kept (claustrum's own, not measured) |
| the deadline ends the run | no SIGTERM (`5fd08069`) | kept (`5fd08069`) |
| the run completes with nothing on stderr | the output decides | the output decides |

The exit code is no rule. When no process holds the file, `lsof` exits 1 with no
text, and the dir then goes. claustrum reads every run that writes to stderr as no
answer, whatever its exit code and stdout. Of those runs, only exit 1 with no
stdout is measured.
After a run that it gave up on, claustrum reads the daemon again, so its pass
takes longer than the pass of `5fd08069`, with the same signals and folders
([D17](DIVERGENCES.md#d17), retired).

When the busy read of a daemon gets no answer, the pass logs these two lines and
counts the daemon under `candidates left undecided`. The first line comes before
every run dir line:

```
[hostclean] daemon pid <pid> left alone this pass: its open files could not be inspected
[hostclean] run dir "<dir>" unused for <n> days: its daemon (pid <pid>) was not retired: a connection is attached to it right now, or that could not be read: in use after all
```

The first line goes only to a daemon that is past claustrum's own age of 5
minutes and serves an idle run dir, once for each such daemon. With more than 4096 processes claustrum
prints no such line (from the code, not measured). On Linux and macOS the second
line also comes for a daemon that still has a client (measured against
`f6010b97`). On Linux it also comes for a daemon whose connections could not be
read (from the code).

When the stdio read of an orphaned Claude Code group gets no answer, the cleaner
does not signal the group. It logs this line after the daemon line and before the
run dir lines, and counts the group as undecided:

```
[hostclean] process group <pgid> left alone this pass: its descriptors could not be inspected
```

With a working `lsof` the cleaner ends the group. A run that the cleaner gave up
on spares the group with claustrum's own line. On Linux the same line comes when
`/proc/<pid>/fd` of the group cannot be read (from the code). On macOS it comes
when a completed `lsof` run lists no file of the group (from the code):

```
[hostclean] spared process group <pgid> this sweep: its open descriptors were unreadable
```

### The lock of an idle run dir

On macOS these states of the `daemon.lock` of an idle run dir keep the dir, with
the line `[hostclean] run dir "<dir>" unused for <n> days: kept, its daemon.lock could not be examined`:

- the `lsof` command does not start, or the run writes to stderr and exits 1, and
  the dir has a `daemon.lock` that is empty, holds the record of a dead daemon,
  or that a live process holds. Any other content takes the same arm (from the
  code)
- the `daemon.lock` has mode 0000
- the `daemon.lock` is a symlink to a regular file, a folder, or a FIFO

On macOS these states keep the dir with the line
`[hostclean] run dir "<dir>" unused for <n> days: kept, a live process holds its daemon.lock`:

- another live process holds the lock file open
- the cleaner and a child of the cleaner hold the lock file open

On macOS the dir goes in these states:

- the dir has no `daemon.lock`, also when `lsof` does not start or writes to
  stderr and exits 1. No `lsof` runs then (from the code)
- only the cleaner itself holds the lock file open
- the lock holds no record (text, `{}`, or the first half of a record), and no
  process holds it
- the lock holds the record of a live pid that does not hold the file
- the lock holds the record of a dead pid, and no process holds it

A second pass is not measured on macOS.

On Linux a lock that cannot be opened, a lock that is no regular file, and lock
content that is no record keep the dir, with claustrum's own line
`[hostclean] run dir "<dir>" kept: whether a process holds its run-dir lock could not be determined`.
On Linux an empty `daemon.lock` is not such content: the dir goes when no live
process holds it. On Linux (`5fd08069`),
when `/proc/locks` cannot be read, a dir whose `daemon.lock` is a regular file,
empty or with a record, stays with the `could not be examined` line, also when a
process holds the lock.

### Passes, orphans and stale run dirs

These rules are read from claustrum's code, except where a bullet names the
reference.

- After the first pass, one pass runs each day. The cleaner
  acts only on this user's own processes and on the `run`, `srv` and `ccd-cli`
  folders of its own root. A pass acts only after it confirms that its own
  socket still leads to itself.
- An orphan is a `stream-json` daemon child that leads its own process group.
  Its parent is pid 1, its binary sits directly in `<root>/ccd-cli`, and its
  stdin, stdout and stderr are pipes. Its group gets `SIGTERM`, a 3 s grace,
  then `SIGKILL`. One pass ends 64 groups at most. A daemon or an orphan
  younger than 5 minutes is never touched. An ambiguous orphan is spared with
  a logged reason.
- A stale run dir is idle past 30 days, or its name carries `.removing-`. Its
  socket does not answer, and no live process holds its lock. The pass reads
  the idle age of each run dir before it dials any daemon, because a dial
  writes a line to that daemon's log. It skips its own run dir and every entry
  that is not a plain directory, a symlink included. It reads the idle age
  again before it probes a dir, and it keeps a dir that came back into use,
  unless the dir has a staging name. It renames a stale dir to
  `<name>.removing-<own pid>-<base-36 time>` inside the run root and then
  removes it. If a socket appears after the rename, or the lock becomes held
  or unreadable, it renames the dir back. A dir whose daemon it retires goes
  in the same pass, once that daemon's socket is gone. A dir it cannot probe
  conclusively stays.
- "Busy" is a 3 s sampling window, not a state. A daemon that is idle across
  the window and gains a client a moment later still gets `SIGTERM`.
- The limit lines are four: more than 4096 processes, more than 64 orphan
  groups, more than 64 run dirs, and 8 retire attempts in one pass. claustrum
  counts attempts there, refused ones included. The reference count after a
  refused attempt is not measured. The retire-limit line and the lines for more
  than 4096 processes and more than 64 orphan groups are the texts of the
  reference (measured on Linux). The log
  lines carry the `[hostclean]` prefix after claustrum's level tag. The line
  before a `SIGKILL` and the reasons for a listener that is not our daemon use
  the texts of the reference (measured on Linux).
- The 15 s delay, the 3 s grace and the limit of 64 groups match the
  reference. The daily period, the 5 minute age and the 30-day idle age are
  claustrum's own.

## Daemon startup (`-serve`)

This section holds what the launcher and the daemon do at a `-serve` start. The
flags are under [-serve, run the daemon](PROTOCOL.md#-serve-run-the-daemon). The launcher
wait and the self-locate are measured against `5fd08069`, and the other rules
against `89cb6289`, on the systems that each rule names.

The launcher is the `-serve` process that the operator starts. It re-execs its own
binary as the detached daemon and waits for it. On Linux a launcher that cannot
read `/proc/self/exe` prints `claustrum: locate self: <error>` and exits `1` before
any write. macOS and Windows re-exec `os.Args[0]` instead (not measured). The
launcher creates a missing socket folder with mode `0700`. It sends the stdout and
stderr of the daemon to the [daemon log](#daemon-log-remote-serverlog).

### The launcher wait

The launcher connects to the socket path every 50 ms, for 12 s at most. It closes
each connection at once, so the log of the daemon that answers holds a
`New connection from: @` / `Connection closed: @` pair. A socket file that refuses
the connect does not end the wait. A killed daemon leaves such a file. A folder
with content at the path does not end the wait either. The daemon cannot remove
it, so it never binds (measured on Linux and Windows). An empty folder there is
removed as an old socket file is (from the code, not measured).

- The predecessor is the daemon that answered on the path before the start. On
  Linux and macOS, a daemon that answers and is not the predecessor ends the wait
  with exit `0`.
- On Windows the first connect that works ends the wait, also one that the
  predecessor answers.
- At 12 s the launcher gives up with exit `1` and prints one of two lines to
  stderr. A connect that reached the predecessor gives
  `claustrum: daemon did not take over <socket> (predecessor still owns it)`. Any
  other wait gives `claustrum: timeout waiting for daemon to accept on <socket>`.

After exit `0` the socket accepts connections. On success the launcher prints
nothing but the Windows breakaway line below. The daemon prints its
[ready line](#daemon-log-remote-serverlog) on its own stdout, which is the daemon
log. When the launcher opens no log, that stdout is the stdout of the launcher
(from the code).

A restart on a live socket unlinks the old socket and binds a new one, off the
JSON-RPC wire. The half of the departing daemon is the identity-tested unlink
under [Token persistence](#token-persistence-daemontoken).

### The bind after the reap (Linux and macOS)

The daemon claims `daemon.lock` and reaps the recorded children of a dead daemon.
Only then does it remove the old socket file, bind, and complete the owner record
of the lock. So nothing listens on the
path while the reap waits for a group to end. A client connect in that wait fails:
with the socket file of the dead daemon there, it gets `ECONNREFUSED`, and with no
socket file, it gets `ENOENT`. A recorded child that ignores `SIGTERM` makes the
wait about 2 s. [Child records and the reap](#child-records-and-the-reap-linux-and-macos)
has the reap itself.

On Windows the daemon also removes the old socket file before it binds. It runs no
reap there.

### The job of the launcher (Windows)

On Windows the launcher starts the daemon outside the job of the launcher
(`CREATE_BREAKAWAY_FROM_JOB`). So the daemon stays alive after an SSH session whose
job ends its processes. How the reference leaves the job is not measured. If the
job refuses breakaway, the launcher starts the daemon inside the job and prints
one line on its stderr. `89cb6289` prints this text after a time stamp, and
claustrum puts its level tag before it:

```
[daemon] detached spawn failed (fork/exec <binary>: Access is denied.); retrying without breakaway
```

### Child records and the reap (Linux and macOS)

A daemon that holds the [run-dir lock](#run-dir-lock-daemonlock) records each [process.spawn](protocol/process-spawn.md) child in `<run dir>/children/<pid>.json`, on every socket shape. The run dir is the folder of the socket path. The child needs a pid of 2 or more and a start time that the daemon can read. Windows writes no record and reaps none (measured on Windows). Unless a rule says otherwise, the rules below are measured against `89cb6289` on Linux, and a rule that names macOS on macOS.

The record is one JSON object with the members in this order:

```json
{"pid":<int>,"node":"<boot-id>/pid:[<inode>]","host":"machine-id:<hex>","instance":"<daemon instance id>","daemonPid":<int>,"daemonStart":"<start>","argv0":"<child argv0>","start":"<start>","at":<epoch-ms>}
```

`daemonStart` and `start` are strings. They hold clock ticks on Linux and the date text of `CLAUDE_SSH_CHILD` on macOS. On macOS the node is the boot-session UUID, and the host is the hostname. With a `launcher`, `argv0` is the launcher, and a `program` member after `argv0` holds the command. claustrum writes no `program` without a launcher (not measured on the reference).

- The daemon writes a record through a temp file `children/.rec-<pid>.json.<12 characters>` and a rename. The source of the 12 characters is claustrum's own.
- The end of the child removes its record, before the pipe drain.
- A graceful shutdown removes the records of the children that it ends. claustrum waits 1 s at most for them (claustrum's own). With `-keep-children` it removes the records of the children that stay alive, so the next daemon leaves them alone (claustrum's own, as the flag is).
- A record stays after a kill of the daemon, and for a process whose `id` a later spawn took.
- After a rename of the run dir, a record goes under the new name. The daemon holds the run dir open.
- No record goes through a `children` that is not a real folder inside the run dir. The remove at the end of the child is the one exception: it follows a symbolic link to a folder inside the run dir.

At a `-serve` start, after the run-dir lock and before the bind, the daemon reads the records:

- It walks `children` in directory order and handles the first 128 names that end in `.json`. The rest wait for the next start. Kept records count toward the 128. So 128 records of another machine, which stay for 30 days, stop every reap, and an orphan behind them stays alive.
- It sends `SIGTERM` to 64 groups at most. The others keep their records for the next start.
- Only a regular file is a record. A link, a FIFO or an empty folder with a `.json` name is removed, with no signal. The target of a link stays. A folder with content stays (from the code).
- It reads at most 4096 bytes of a record. A record whose first 4096 bytes are not one JSON value is removed, with no signal.
- If `children` is not a real folder, it reads, signals and removes nothing. That is measured for a symbolic link to a folder. The other kinds are claustrum's own.

A record of a daemon that is alive stays. So does a record from another machine, for 30 days (from the code). On macOS a `daemonStart` with two blanks before a one-digit day does not match the live daemon, so a new daemon reads that live daemon as gone. An older claustrum build wrote such a text on days 1 to 9 of a month. The children of that daemon then take the test below, and one whose `start` matches gets `SIGTERM`. A record of a dead pid is removed. So is a record from an earlier boot of this machine (from the code). For a record of a dead daemon on this machine, the daemon tests the process. If it still is the recorded child, its group gets `SIGTERM`. Else the record goes with no signal. The test has four parts:

- The process started at the recorded `start`, so the pid is not reused. On macOS a `start` with two blanks before a one-digit day does not match.
- It leads its own process group.
- It runs `argv0`. With a `program` member, it can also run `program`, or hold `program` as one whole argument. On Linux the program is the first argument of `/proc/<pid>/cmdline`, and a name with the same last path component matches. On macOS the daemon reads the command text of `ps`, which cuts words at blanks. So a `program` with a blank never matches an argument there.
- `CLAUDE_SSH_RUN_DIR` names this run dir, and `CLAUDE_SSH_CHILD` names the pid and start of the process itself.

A process that the daemon cannot inspect keeps its record and gets no signal (`5fd08069`, from the code). The daemon does not test the owner of a record file.

After the `SIGTERM` the daemon polls for 2 s. At each poll a group takes the whole test again. A group that fails it gets nothing more. A group that still passes after 2 s gets `SIGKILL`. Then the daemon waits 1 s at most for the group to end (claustrum's own value). After the last group is done, the records of all signalled groups go together. A leader that reads as gone is read again after 50 ms before the `SIGKILL` of its group ([D20](DIVERGENCES.md#d20)). A group that still cannot be inspected at the end, or that outlives the `SIGKILL`, keeps its record. The [log lines](#lines-of-child-processes) of the reap are under the daemon log.

## Idle-connection close

A `-serve` daemon closes any accepted connection that goes 5 minutes with no read
or write. This matches `f6010b97` (measured on Linux). `5fd08069` closes at the
same times as claustrum (measured on Linux and macOS). The timeout is fixed and
always on, with no flag and no configuration key. A watcher of each connection
checks at a quarter of the timeout, capped at 30 s. At the timeout it closes the
socket and logs `[Server] closing connection idle <d> in both directions:
<addr>`. `89cb6289` logs that text with `5m0s` and `@` (measured on Linux). The watcher ends with its
connection and at shutdown. The close sends no frame, and it never ends the
daemon. On Linux and macOS the [orphan-exit self-probe](#orphan-exit-self-probe)
retires a daemon with no client.

## Daemon log (`remote-server.log`)

The rules of the file and the peer-check lines are measured against `5fd08069`,
and the startup lines against `f6010b97` and `89cb6289`. A rule that names one
system was measured on that system, and an unmarked rule on Linux and macOS.

At each start the launcher renames an existing `remote-server.log` in the
socket's directory to `remote-server.log.old`. Then it creates a fresh log there
with mode `0600`. It sends the stdout and stderr of the daemonized child into
that file, so its own streams stay empty, except for the launcher's own
failures. Those are under [Daemon startup](#daemon-startup-serve) and
[The token source](PROTOCOL.md#the-token-source), and a log that cannot be created gives
`claustrum: open log: <err>`. claustrum does not remove the log at shutdown (from
the code), unlike the socket and `daemon.token`. So the log of the last run stays
readable. The fixed name and location are the deployment contract, and they are
not configurable.

claustrum writes a level tag before each line of its logger. The banner and a
start error that begins `claustrum:` have none. The reference writes no tag. `CLAUSTRUM_LOG_LEVEL=warn` drops the INFO lines.

When the raise of the open-files limit works on Linux and macOS, the first line
tells the limit that `process.spawn` children get. It is the lower of 65536 and
the hard limit. On Windows there is no such line. The ready banner follows, with
no timestamp:

```
2026/07/31 00:17:30 INFO  [daemon] child processes will start with an open-files limit of 65536
Claustrum remote server listening on /run/user/1000/claude/rpc.sock (pid 4711, instance 5f1c0a9e2b7d4c3e8a6f0b1d2c3e4f50)
2026/07/31 00:17:30 INFO  [daemon] host cleaning off: socket path "/run/user/1000/claude/rpc.sock" is not <root>/run/<id>/rpc.sock
2026/07/31 00:17:30 INFO  [Server] New connection from: @
```

A failed raise writes no line (claustrum's own, not measured). The banner ends
with the pid of the daemon and its instance, the `instanceId` of
`server.capabilities`. The name `Claustrum` is claustrum's own, and the
reference prints `Claude` there.

A start with `CLAUDE_SSH_PEER_CHECK=1` writes one more line before the banner.
On Linux it is at INFO level, comes after the open-files line, and names the
namespaces of the daemon:

```
[Server] serving only callers in "user:[4026531837] net:1 pidfd"
```

On macOS and Windows the line is at WARN level and comes last before the banner,
with no reap line between them. Its place beside the lines of a reap is not
measured:

```
[Server] this system names no namespaces: cannot tell callers in a sandbox from others, serving all
```

A Linux kernel that names no namespace gets that line too (claustrum's own, not
measured).

If the fresh create fails, the launcher judges the entry at the path without
following it, and it opens no FIFO:

- Nothing at the path fails the start with the error of the create (measured on
  Linux).
- A folder fails the start with `is a directory`.
- An own regular file is truncated. It keeps its mode and gets the lines.
- An own link to an own regular file gets the same: the file is truncated
  (measured on Linux). On Windows a link fails with `permission denied`
  ([D8](DIVERGENCES.md#d8)).
- Every other entry fails the start with `permission denied` and stays
  unchanged. That is [D8](DIVERGENCES.md#d8).

A failed start prints one line on stderr and exits 1. The reference prints the
same refusal with its own prefix. No daemon, socket, `daemon.lock` or
`daemon.token` follows it (from the code):

```
claustrum: open log: open <path>: is a directory
```

On Windows the rename moves a folder onto a regular `.old`. A second daemon can
start on a live socket there. The first daemon holds the log open, so the
rename fails, and the launcher truncates the log. The second daemon then logs
there. Each daemon writes at the offset of its own handle. So the earlier lines
of the first daemon are lost, and its later lines sit behind a block of NUL
bytes. A log that another process holds with no delete share is truncated too,
and `.old` stays. This is parity ([D21](DIVERGENCES.md#d21) is retired).

### Lines of child processes

These lines tell of [process.spawn](protocol/process-spawn.md) children and of their [records and the reap](#child-records-and-the-reap-linux-and-macos). The levels are claustrum's own. Unless a row says otherwise, each text is the text of `89cb6289`, measured on Linux. The group-ended, outlived and dropped-record lines are measured on macOS too.

| Event | Level | Line |
|---|---|---|
| A start | INFO | `[process.Manager] Process <id> started, PID=<n>, command=<command>` (from the code). With a launcher, ` via launcher <argv0>` follows. |
| A failed start | ERROR | `[process.Manager] Failed to start process <id>: <message>`, with the text of the error frame |
| An exit | INFO | `[process.Manager] Process <id> exited with code <n>`, then `, terminated by <signal>` for a signal and `, signalled at client request` or `, signalled at shutdown request` for a kill by the daemon |
| The drain limit | WARN | `[process.Manager] Process <id>: pipe drain grace expired (grandchild holding stdio?); force-closing` |
| A supersede | INFO | `[process.Manager] process <new id> supersedes <old id> for session <key>: died=<bool> escalated=<bool> alreadyExited=<bool>` (from the code), after the exit line of the old process |
| `children` not a folder | ERROR | `[process.Registry] <run dir>/children is not a directory; recording nothing`, or `reaping nothing` at a start |
| A record write fails | ERROR | `[process.Registry] cannot record child <pid>: <error>` |
| A record remove fails | WARN | `[process.Registry] cannot remove record of child <pid>: <error>` |
| Over 128 records | WARN | `[process.Registry] more than 128 records in <run dir>/children; leaving the rest for the next start` |
| Over 64 groups | WARN | `[process.Registry] <n> more orphaned group(s) recorded in <run dir>/children than the 64 one start ends; leaving those for the next start` |
| A record dropped | INFO | `[process.Registry] record <pid>.json: pid <pid> is not a child to end (<reason>); dropping the record, signalling nothing` |
| A record kept | INFO | `[process.Registry] record <pid>.json: pid <pid> cannot be inspected now (<reason>); keeping the record, signalling nothing` (`5fd08069`, measured on Linux) |
| A group ended | INFO | `[process.Registry] ending orphaned process group <pgid> recorded by daemon instance "<instance>" (pid <daemon pid>): SIGTERM` |
| The grace ran out | WARN | `[process.Registry] group <pgid> outlived SIGTERM for 2s: SIGKILL` |
| A group fails the test | INFO | `[process.Registry] group <pgid> no longer verifies (<reason>); nothing more is sent to it` |
| The reap summary | INFO | `[daemon] serve: predecessor's children — <n> orphaned group(s): <a> ended on SIGTERM, <b> on SIGKILL, <c> survived; <d> stale record(s) dropped, <e> kept` |

The exit line is a race at a shutdown, on the reference and on claustrum. It can be missing. A reap that sent no `SIGTERM` and kept no record writes no summary.

The main `<reason>` texts of a dropped record and of a group that fails the test are these:

- `it does not run the recorded program "<argv0>"`, with `and neither runs nor was handed "<program>"` after it for a record with `program`
- `it does not lead its own process group`
- `it did not start at the recorded "<start>": the pid has been reused`
- `it was not started by a daemon of this run dir: no CLAUDE_SSH_RUN_DIR names it`
- `it belongs to a daemon of another run dir: its CLAUDE_SSH_RUN_DIR names a different one`
- `it was not started by a daemon itself: its CLAUDE_SSH_CHILD does not name its own pid and start (something a child started)`
- `environment unreadable: <error>`, and on `5fd08069` `pid namespace unreadable: <error>` and `stat unreadable: <error>`
- `the record names no program to verify against`
- claustrum's own: `pid is out of range`, `pid is this daemon`, `pid is this daemon's parent or process group` and `record has no start time to verify against`

## Orphan-exit self-probe

On Linux and macOS a `-serve` daemon whose socket path no longer leads back to it
shuts down, as on `4534d86`. The probe is off the JSON-RPC wire. Its effects are
log lines and, once orphaned, a self-directed authed `server.capabilities`
request.

Every 60 seconds the daemon compares the file identity of its socket path
(`os.SameFile`) to the inode it bound. The check interval of the reference is
not measured. If the path is gone or another inode, a
10-minute grace clock starts. A path that still matches resets the clock. A check
that sees a client starts the clock again and writes no line. So the first line
below does not come after that client leaves, as on `5fd08069` (measured on Linux
and macOS). After the grace with nobody connected, the daemon dials its socket
and compares the `instanceId` of the reply to its own. A probe that reaches itself
resets the clock. Two failed probes, 60 seconds apart, start a graceful shutdown.
It closes listeners, drops clients, and stops child process groups unless
`-keep-children` is set. `89cb6289` and claustrum exit with code 0, with no
signal from another process (measured on Linux). `89cb6289` removes
`daemon.token` (measured on Linux). claustrum removes it through its graceful
stop (from the code).

These are the log lines of `89cb6289` in one run, and claustrum logs the same
texts with its own values (measured on Linux):

```
[Server] socket path <socket> no longer leads to this daemon; shutting down if that holds for 10m0s with nobody connected
[Server] socket path <socket> did not lead back to this daemon (1/2); re-checking before acting
[Server] orphaned for 11m0s (socket path gone or re-bound, 2 self-probes failed, no connections); shutting down and killing 0 child process(es)
[Server] shutdown requested
[Server] cleanup: closed 0 connection(s), killed 0 child process group(s)
```

On `89cb6289` the second line came about 10 minutes after the first, and the
third one minute later (measured on Linux). The reference logs `orphaned for
11m0s` or `12m0s` (measured on Linux). claustrum logged `12m0s` (measured on
Linux), and the code prints `11m0s` at exact ticks. The cause of that minute is
not measured. At the self-exit `89cb6289` ends the children with `SIGKILL`, and
claustrum ends them too (measured on Linux).

A check with no client also ends a daemon after 30 days with no counted request,
on the monotonic clock. Each request past auth counts, except
`server.capabilities`. This idle exit is not measured on a reference build.

The checks run on Linux and macOS only. On Windows a daemon whose socket file was
deleted writes no line. `5fd08069` wrote no line and was still alive after 14
minutes (measured on Windows).
