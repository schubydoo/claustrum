# -install and -probe-cli

This page holds the two modes that put the agent CLI in place and test it. The
other modes and the flags table are under
[Daemon lifecycle (flags)](PROTOCOL.md#daemon-lifecycle-flags).

A rule with no mark matches the reference as measured on `5fd08069` or on an
earlier build. A build that a section names in its first lines holds for that
section. The other marks are those of the [protocol reference](PROTOCOL.md).

## -install — ensure the agent CLI

```text
claustrum -install -cli-version <v> [-cli-dir <d>] \
          [-cli-url <u> -cli-checksum <sha256>] [-cli-zst <p>] [-cli-keep <n>] \
          [-max-cli-bytes <n>] [-cli-download-timeout <dur>]
```

`-install` makes sure that one CLI version is in the cli-dir. On a cache miss it
downloads or reads the CLI, extracts and verifies it, puts it in place and runs
`<cli> --version` once. Then it prunes old versions and prints one
`__INSTALL_RESULT__<json>` facts line (schema in [`__INSTALL_RESULT__` facts](#__install_result__-facts)).
The builds ran on Linux, macOS and Windows. A rule that names one system, or
that sits under a lead-in that names one, was measured on that system only.
[REFERENCE-BUILDS.md](REFERENCE-BUILDS.md) has the history of most of these rules.

| Flag | Default | Meaning |
|---|---|---|
| `-cli-version <v>` | none | The version to ensure. It names the CLI file. Without it the `cliPath` is empty, no CLI runs and no folder is made |
| `-cli-dir <d>` | `<home>/.claude/remote/ccd-cli` | The folder of the version files. An empty value takes the default too |
| `-cli-url <u>` | none | Download the zstd archive of the CLI from this URL |
| `-cli-checksum <sha256>` | none | The SHA-256 of the archive |
| `-cli-zst <p>` | none | Read the zstd archive from this file, the blob |

`-cli-keep`, `-max-cli-bytes`, `-cli-download-timeout` and the deprecated
`-cli-probe-timeout` and `-libc-probe-timeout` are in
[Flags and config keys](PROTOCOL.md#flags-and-config-keys). `-install` reaches the network only
with `-cli-url`.

`-install` exits `0` whenever it prints the facts line. It reports a failure inside
the facts as `cliError`, not through the exit code. Three cases print no facts
line. A flag that does not parse exits `2`. `-cli-version` with no `-cli-dir` and
no home folder exits `2`. Any other run with no home folder exits `1`. With no `HOME`
the reference prints `claude-ssh: cannot resolve home directory: $HOME is not defined`
on stderr (Linux). claustrum prints the same line with its own prefix,
`claustrum: `. On Windows its line names `%userprofile%`. The exit `1` cases, macOS and a missing `USERPROFILE` are not
measured.

The `cliError` catalogue follows. Every error that `ensureCLI` returns lands in
`cliError` verbatim, with the wrapping prefix that its phase adds. That is the
form a driver sees:

| `cliError` | trigger |
|---|---|
| `installed cli at <path> is not runnable` | the `--version` run of a new CLI did not start or exited non-zero. `<path>` is the final path |
| `cli <v> missing and no --cli-url or --cli-zst provided` | cache miss with no source flag |
| `cli unresponsive: the installed Claude Code binary started but did not answer --version within 30s (120s for a first run) and was stopped; the host is not letting it run (endpoint security software or a stalled network home are the usual causes)` | a direct `--version` run stopped at 30 s (120 s after an install in the same run). Always-on, parity (see below) |
| `checksum mismatch: expected=<x>, actual=<y>` | `-cli-checksum` verify failed. It applies to every `-cli-url` run. With a supplied checksum it applies to `-cli-zst` too. The compare is case-sensitive |
| `opening input: <err>` | `-cli-zst` open error |
| `decompressing: <err>` | bad zstd blob or body, for example `invalid input: magic number mismatch`. A `-cli-zst` folder gives `read <p>: is a directory` (Linux) |
| `decompressing: decompressed CLI exceeds <n> bytes` | D10 cap, opt-in |
| `staging cli: <err>` | no staging file can be made in the cli-dir (claustrum's own, not measured) |
| `download failed with status <n>` | the server answered a status other than 200. The text has no URL and no reason phrase |
| `download failed: response exceeds <n> bytes` | D10 cap on the download body, opt-in |
| `download failed: <transport err>` | a transport error before the body, for example a refused connection. The prefix `download failed: ` also wraps every other download error that has no bare form in this table |
| `download failed: Get "<url>": net/http: timeout awaiting response headers` | no response headers for 60 s (always-on, parity, see below) |
| `download interrupted after <got>/<total> bytes: <transport err>` | a body read that ended with a transport error, for example `read tcp …: read: connection reset by peer` (parity, see below) |
| `download failed: context deadline exceeded (Client.Timeout or context cancellation while reading body)` | D12 download deadline, opt-in |
| `download stalled: no data for 60s after <got>/<total> bytes` | read-idle abort, meaning no bytes for 60 s on the `-cli-url` body (always-on, parity) |
| `mkdir cli dir: <err>` | cli-dir uncreatable. `cliPath` is then empty. A file or a FIFO at the cli-dir path is replaced first (see Staging and cleanup) |
| `cli version "…" must be a single path component` | D6 hardening |
| `cli path must not be or contain the home directory: "<path>"` | a folder at the final path that is the home folder or one of its parent folders (D2 hardening) |
| `clearing stale dir at <path>: <err>` | an occupied `cliPath` directory that claustrum cannot remove |
| `rename: <err>` | the rename of the new CLI failed, for example `rename: rename <tmp> <cli>: Access is denied.` on Windows. The prefix on Linux and macOS is claustrum's own |
| `rename: staging file vanished before install: <err>` | a concurrent sweep took the staging file |
| `cli unresponsive: the installed Claude Code binary was started through the host's managed launcher <argv0> and the run did not answer --version within 33s (123s for a first run), so it was stopped; the launcher or the host is not letting it finish` | a managed launcher run stopped at 33 s (123 s after a fresh install), with `CLAUDE_SSH_MANAGED_LAUNCHER=1` (see below) |

The download forms use different wording on purpose, to match the reference. The
status, stall and interrupted forms are fully worded and go out bare.
`staging cli:` and `decompressing:` also go out bare on `-cli-url`. Every other
download error carries the `download failed: ` prefix. Those other errors are a
transport failure before the body, the response-header limit, the D10 cap, the
D12 deadline. The
status form omits the URL, so a signed URL cannot reach whatever captures the
`__INSTALL_RESULT__` line. A bare `chmod` failure propagates as the raw
Go error.

These strings are not free-form diagnostics: the driver reads them. A change to a
row's wording can change what a user sees. No JSON-RPC frame has to move. A
new guard whose error pre-empts a row can do the same. D10's opt-in cap is the
worked example. How the driver classifies them is a third-binary claim. See
[Driver claims and their provenance](ARCHITECTURE.md#driver-claims-and-their-provenance).

The managed launcher (measured on Linux, unless a rule names its system):
- `-install` uses the [managed launcher](protocol/launcher-resolve.md) only when
  `CLAUDE_SSH_MANAGED_LAUNCHER` is exactly `1`. Every other value, and an unset
  variable, adds no field and runs no launcher. Values other than `true` and `1 `
  (a trailing space) are not measured.
- With the gate and a CLI path, `-install` resolves the launcher for that path, as
  [launcher.resolve](protocol/launcher-resolve.md) does. The facts line then gains
  launcher fields after `cliWasPresent`, `cliError` and `cliUnresponsive`, in this
  order: `launcherStatus`, `launcher`, `launcherSource`, `launcherPath`,
  `launcherReason`, `launcherStderr`. A field that does not apply is omitted. With
  `-cli-url`, `fetch` comes after `cliUnresponsive` and before `launcherStatus`
  (Linux).
- An empty `cliPath` gains no launcher field. That holds without `-cli-version`,
  where the line ends `"cliPath":"","cliWasPresent":false}`, and for a cli-dir that
  cannot be made (Linux).
- With `-cli-version` and no `-cli-dir`, the CLI path is in the default CLI folder,
  and the launcher gets that path (macOS).
- `none`: the CLI runs directly under the direct bounds below, with the gate in its
  env. The facts end `"cliWasPresent":true,"launcherStatus":"none"}`. A stopped
  direct run ends `"cliUnresponsive":true,"launcherStatus":"none"}`. On Windows the
  resolve always answers none, so `-install` appends `"launcherStatus":"none"` last
  and runs the CLI directly.
- `usable`: the CLI runs once, as `<launcher argv...> <cli> --version`. No direct
  run happens. The launcher's stdout is not reported, and its stderr does not reach
  `-install` stderr. The facts add `launcher` and `launcherSource`.
- `unusable` or `unreadable`: the CLI does not run at all. `cliWasPresent` stays
  true, and there is no `cliError`. The facts add `launcherSource` or
  `launcherPath`, and `launcherReason`.
- `probe_failed`: the launcher run exited non-zero or ended on a signal.
  `launcherReason` is `exit status <n>` or `terminated by SIGTERM`. The CLI file
  stays, `cliWasPresent` stays true, and there is no `cliError`. `launcherStderr`
  holds the launcher's stderr. If that is empty, the field is omitted.
- `launcherStderr` keeps 8192 bytes. More stderr adds `\n[… <n> more bytes]`, with
  the JSON escape `\n` on the line and `…` as raw `e2 80 a6`. `<n>` is the count of
  dropped bytes. claustrum's own (not measured): it keeps the first bytes, and it
  adds the `\n` also after a kept part that ends in a newline.
- On a cache hit a launcher run that has not ended at 33 s is stopped. The facts
  then read `"cliWasPresent":false`, the launcher `cli unresponsive: …` text above,
  `"cliUnresponsive":true`, `"launcherStatus":"unresponsive"` and
  `"launcherReason":"did not exit within 33s and was stopped"`. The CLI file stays,
  no install follows (with a source flag, claustrum's own), nothing is pruned and the sweep runs (Linux and macOS). With
  a usable launcher the direct bounds do not apply (Linux and macOS).
- The run right after a fresh install has a bound of 123 s (Linux). A stopped run
  gives the same facts with
  `"launcherReason":"did not exit within 123s and was stopped"`. The CLI stays
  installed, the blob is consumed and nothing is pruned.
- A fresh install puts the CLI at its final path and then runs it once through a
  usable launcher, which gets the final path. The facts read
  `"cliWasPresent":false` with the usable fields (Linux and macOS). A failed run
  and an unusable launcher still leave the CLI installed (Linux, `-cli-url`).
  claustrum does the same for an unreadable answer and for `-cli-zst` (claustrum's
  own).
- On a cache hit on Linux the CLI file must be a regular file with an exec bit
  that starts with `#!` or an ELF header of this machine. Another file does not
  run, and the install runs over it. The reference does that for a text file and
  for an ELF of another machine. The test beyond those two files is claustrum's
  own.
- The launcher run gets the daemon env without `CLAUDE_SSH_MANAGED_LAUNCHER`
  (Linux and macOS). claustrum also drops `CLAUDE_CODE_PROCESS_WRAPPER` and
  `CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR` there, as for a spawn with a launcher
  (claustrum's own).
- claustrum's own (not measured): the stop kills the run's whole process group and
  then waits up to 2 s for its output. The texts name the launcher's argv[0] only.
- `-install` still exits `0` with an empty stderr, and it writes no
  `[LauncherHandler]` line.

The direct `--version` run and its bounds are parity. A direct run is a run with
no usable launcher: the gate is unset, or the launcher status is `none`.
- On a cache hit a run that has not ended after 30 s is stopped. The facts then
  read `"cliWasPresent":false`, the direct `cli unresponsive: …` text and
  `"cliUnresponsive":true`. `-install` exits `0` with an empty stderr.
- The run of a CLI that this run installed is stopped at 120 s, with the same text.
  A new CLI that replaced a file at the final path that did not run gets the same
  bound (Linux).
- The download time is outside the bound (Linux and Windows). claustrum starts the
  clock right before the process start call.
- On Linux and macOS the CLI runs in a process group of its own, in the session of
  `-install`. A CLI that ignores SIGTERM stops at the same time. claustrum kills
  the group with SIGKILL (claustrum's own). A child in a new session survives.
- On Windows the stop ends the CLI process, and its children stay alive.
- A process that holds the output of the CLI after the CLI exited delays nothing.
  claustrum gives the CLI the null device as stdin, stdout and stderr (claustrum's
  own).
- After a stopped cache hit the CLI file stays, nothing is installed or pruned, a
  `-cli-zst` blob stays and the sweep runs. A `-cli-url` server gets no request
  (Linux and Windows).
- After a stopped first run the new CLI stays at its final path, the `-cli-zst`
  blob is consumed and nothing is pruned. With `-cli-url` the facts carry `fetch`
  after `cliUnresponsive`.

The default CLI folder:
- With `-cli-version` and no `-cli-dir`, or an empty `-cli-dir`, the CLI folder is
  `<home>/.claude/remote/ccd-cli`. On Windows `<home>` is `USERPROFILE`, and `HOME`
  is not read. The other steps work there as in an explicit folder.
- A cache miss with a version that D6 accepts makes the folder chain, with or without a source flag. Each new
  level gets mode `0700`, also with umask `000`, and a level that exists keeps its
  mode (Linux and macOS).

The CLI file name on Windows:
- The CLI file is `<cli-dir>\<version>.exe`. The `cliPath`, the file on disk, the
  path of the `--version` run and the `not runnable` text all carry that name. The
  `missing` text names the version with no suffix.
- The suffix is added always, so `-cli-version 9.9.9.exe` installs `9.9.9.exe.exe`.
  A version that ends in `.EXE` is not measured.
- Only `<version>.exe` counts as present. A file with the bare name counts for
  nothing and does not run. So a CLI at the bare name answers `missing` until a
  source flag installs the `.exe` file beside it.
- A folder at the bare name stays. A folder at the `.exe` name is replaced by the
  CLI file.

Download progress and `fetch` stats, on the `-cli-url` path:
- While downloading, `-install` prints `__INSTALL_PROGRESS__<json>` lines to stdout:
  `{"phase":"download","bytes":<n>[,"total":<m>]}`. `total` carries the
  Content-Length. If the server sends none, `total` is dropped.
- A leading `bytes:0` line comes after the response headers and before any body
  byte. A server that sends no headers gets no line (Linux and Windows). A final
  `bytes:<n>` line comes only after the checksum passes. A failed check prints no
  final line (Linux).
- Between those two lines a tick comes each 1 s. If the byte count of a tick equals
  the count of the last printed line, the tick prints nothing. The counts jump irregularly, so a
  consumer treats them as progress, not as a byte-exact sequence. claustrum prints
  the final line also when a tick printed that count already, and it starts the
  tick clock at the first readable body (claustrum's own).
- The facts line gains a `fetch` object after `cliError` and `cliUnresponsive`,
  `{"bytes":<n>,"ms":<n>,"longestPauseMs":<n>}`. Those are bytes read, download
  duration and the largest gap between reads. It is the last field, except with
  the managed launcher gate (see above). It appears whenever a download was
  attempted, even a 0-byte 404. A `-cli-zst` install and a cache hit have no
  `fetch`.
- claustrum does not close its idle connections after the download. Its transport
  is a copy of the Go default transport, whose idle limit is 90 s.
- The wait for the response headers has an always-on 60 s limit (Linux and
  Windows). A server that sends nothing fails the install with
  `download failed: Get "<url>": net/http: timeout awaiting response headers`. No
  progress line is printed, and `fetch` reads `"bytes":0` and
  `"longestPauseMs":0`.
- A body read that ends with a transport error fails the install with
  `download interrupted after <got>/<total> bytes: <transport err>` (Linux). The
  wait from the last byte to the error counts for `longestPauseMs`. One progress
  line is printed, and the cli-dir stays empty. claustrum words other read errors
  the same way, with a `<total>` of `0` for a body with no length (claustrum's
  own).
- The body has an always-on 60 s read-idle abort, reset on every byte. It fails
  the install with the `download stalled: …` text above. It is no total deadline,
  so a slow download that keeps sending completes. The total deadline is the
  opt-in [D12](DIVERGENCES.md#d12), off by default. When armed, it bounds the
  whole exchange, so an honest download that is too slow fails with
  `download failed: context deadline exceeded (…)`.

Checksum and verify ordering:
- claustrum verifies `-cli-checksum` on the `-cli-url` path unconditionally. An
  empty checksum still fails.
- The source is decompressed into the staging file first, and the checksum comes
  after it. A body that is no zstd archive answers
  `decompressing: invalid input: magic number mismatch` with a right and with a
  wrong checksum. `fetch` then counts the bytes that the decoder read, and only the
  leading progress line is printed (Linux). No chmod, clear or rename runs before
  the checksum passes (claustrum's own).
- If a `-cli-checksum` is supplied, claustrum also verifies the `-cli-zst` blob,
  as the reference does ([D1](DIVERGENCES.md#d1), retired). A mismatch
  keeps the blob. An absent or empty checksum verifies nothing.
- The compare is case-sensitive on both paths. The right digest in upper case
  answers `checksum mismatch: expected=<UPPER>, actual=<lower>` (Linux).
- Every cache miss with a CLI path and a version that D6 accepts makes the cli-dir
  first (mode `0700`, with its parents). That is before the source check, before
  the blob opens and before any network access (Linux). So after that
  step a failed cache miss leaves the cli-dir. A new cli-dir stays empty.
- On Linux and macOS that folder step also runs on a cache hit. Windows makes the
  cli-dir on a cache miss only. On Linux and macOS the step uses the `-cli-dir` text
  as given, so `-cli-dir <dir>/sub/..` makes `<dir>/sub` (mode `0700`). On Windows
  `<dir>/sub` is not made. If the step fails, `cliPath` is empty and `cliError` is the mkdir error,
  such as `mkdir cli dir: mkdir <dir>/sub: not a directory`.

The other clocks: each `-install` clock on this page is a clock of the reference and is
always on. The opt-in D12 deadline is one exception. The 2 s output wait of a
stopped launcher run and the 0.2 s rename retry are the others (claustrum's own).
[Conventions for opt-in divergences](DIVERGENCES.md#conventions-for-opt-in-divergences)
has the stdlib transport clocks of `-cli-url`.

[D10](DIVERGENCES.md#d10) is the opt-in size cap of `-max-cli-bytes`, on the
decompressed CLI and on the download body. It is off by default.

`-cli-version` hardening is [D6](DIVERGENCES.md#d6):
- The version must be one path component. Otherwise claustrum answers
  `cli version "…" must be a single path component` (claustrum's own). It answers
  before the install writes or removes anything at a path built from the version,
  and the blob stays. `2.0.0-beta.1` and `1.0.86+build.5` are accepted too. The
  entry has the refused forms, the other accepted forms and what the reference
  does.
- A regular file at the joined path is first run with `--version`, as the cache-hit
  check. A run that passes answers `"cliWasPresent":true` with that path, as on the
  reference (Linux and macOS). A stopped run answers the unresponsive text. D6
  answers only when that run fails, or when no file is there.
- The two sweeps of the cli-dir name only the cli-dir, and they are not behind
  this rule.
- A version with a sweep name, such as `.fetch-1` or `1.0.0.zst`, installs. The
  same run keeps it, because the sweep removes only old entries. A later install
  removes it once it is more than 10 minutes old. On Windows the file is
  `<version>.exe`, and the sweep keeps it.

Staging and cleanup:
- claustrum stages the CLI at `<cli-dir>/.fetch-<random>` and renames it to the
  final path. That is one code path for `-cli-url` and `-cli-zst`. A download is
  decompressed into that file as it arrives, and no other file is made.
- An install puts the new CLI at its final path, removes the `-cli-zst` blob and
  runs the 10 minute sweep. Then `<final path> --version` runs. The prune follows a
  good run only. A direct run that does not start or exits non-zero removes the new CLI again with one plain
  remove. A stopped run keeps the new CLI. On Windows claustrum can leave that file
  when the exited CLI holds it (claustrum's own).
- What is at the final path goes before the new CLI runs. A folder there is removed
  as a tree, with its content. A file there that does not run is replaced by the
  new CLI (Linux). A new CLI that then exits non-zero leaves nothing at that path
  (claustrum's own).
- claustrum clears an occupied `cliPath` only when it is a directory, because
  `rename(2)` refuses to replace a directory. A regular file is replaced
  by the rename. A directory that claustrum cannot remove answers
  `clearing stale dir at <path>: <err>`. On Windows the cleared name is
  `<version>.exe`. The D6 rule runs first, so `cliPath` is a direct child of the
  cli-dir.
- claustrum does not remove a folder at the final path that is the home folder or
  contains it ([D2](DIVERGENCES.md#d2)). It answers
  `cli path must not be or contain the home directory: "<path>"`. The folder
  stays, the blob stays and no CLI runs. The reference removes that folder as a
  tree and puts the CLI file there. That is measured with `-cli-zst` on Linux,
  macOS and Windows, and `-cli-url` is not measured.
- That guard runs the lexical test of the RPC paths first. Then it compares the
  folder at the final path with the home folder and with each parent folder of it,
  by file identity, for the home path as given and for its resolved path.
- On macOS the volume ignores letter case, so a `-cli-version` in another letter
  case names a folder on disk. Both binaries replace such a folder that does not
  hold home, and the new file takes the name of the command. For a folder that
  holds home, claustrum refuses as above.
- The `-cli-zst` blob goes with one plain remove once the new CLI is in place.
  Every earlier failure keeps it: a blob that is no zstd archive, a folder that
  cannot be cleared and a denied rename. claustrum keeps it after a failed chmod
  too (claustrum's own). A CLI that then does not run still costs the blob.
- On Windows a rename that access denied stops is tried again for 0.2 s. If the
  target then holds the bytes of the staging file, the install counts as done and
  the staging file goes. The 0.2 s bound and the pauses are claustrum's own.
- A cli-dir path that names no folder is replaced. The trigger is a regular file, a
  symlink to a regular file or a FIFO at the cli-dir path. `-install` removes that
  entry, and for a symlink it removes the link and keeps the target. It also
  removes the regular file named exactly `ccd-cli-version` in the parent folder of
  the cli-dir. Then it makes the cli folder with mode `0700` and goes on. That holds
  for the default CLI folder too, and with no source flag.
- The name `ccd-cli-version` is literal. A file with any other name stays.
- A folder, a symlink to a folder and an absent cli-dir trigger nothing, and
  `ccd-cli-version` stays. A dangling symlink triggers nothing either, and the
  answer is `mkdir cli dir: mkdir <path>: file exists` with an empty `cliPath`.
  With a parent folder that is not writable nothing is removed, and the answer is
  `mkdir cli dir: mkdir <path>: not a directory`. Those `mkdir` texts are the Linux
  and macOS texts. Windows has no FIFO case and no case with a parent that is not
  writable.
- Both removes are plain removes of one path, never a tree. claustrum's own (not
  measured): it removes the version file first, and if that fails, the cli-dir
  entry stays. It leaves a socket, a device,
  a symlink to a FIFO and a `ccd-cli-version` that is a folder or a symlink alone.
- The `-cli-keep` prune:
    - Only a version file counts, and only a version file goes, with one plain
      remove, never as a tree. On Linux and macOS that is a regular file whose name
      is a version such as `1.0.0`, with an exec bit. On Windows it is a regular
      file `<version>.exe`. Every other entry stays and takes no keep place.
    - claustrum counts a name of three runs of digits with dots between them. Other
      names are not measured.
    - The order is the mtime, newest first. Entries with one mtime stay in byte
      order of the name, so the later name goes. The new CLI takes its place in the
      order and always stays. `-cli-keep 0` and a negative value remove every
      version file but the new CLI, and the result line follows with exit `0` and an empty stderr.
    - A failed install, a stopped run and a cache hit do not prune.
- Two sweeps remove old regular files of the cli-dir with one plain remove each,
  never as a tree. A folder and a link stay (from the code for a link). A
  `<version>` is a version name as in the prune:
    - The 10 minute sweep takes `.fetch-<digits>` and `<version>.zst`.
    - The 7 day sweep takes `<version>.zst.part`.
    - The 7 day sweep runs once before any CLI runs, also on a cache hit. On a good
      cache hit only that sweep runs. The 10 minute sweep runs before the run of a
      new CLI, after a failed attempt and after a D6 refusal. On a cache hit it runs
      only after a run that its bound stopped.
    - Neither sweep removes the `-cli-zst` blob, even a blob named
      `<version>.zst` in the cli-dir.
    - The names match the letter case (claustrum's own). A future mtime is not old (Linux).
    - A concurrent install can take a staging file only once it is more than 10
      minutes old. claustrum then retries a `-cli-zst` staging step once.
- The sweeps and the prune skip an entry of the cli-dir that is the home folder or
  holds it ([D2](DIVERGENCES.md#d2), claustrum's own). The reference removes no
  folder there, so a home folder stays there too (derived from its measured runs,
  none with a home folder).

The libc probe fills the `libc` fact:
- claustrum runs `ldd` on every probe, and its output decides. A "musl" banner
  reports `musl`, and any other output reports `glibc`. The `/lib/ld-musl-*.so.*`
  marker is read only when `ldd` produced no output.
- The probe bounds ldd itself at 5 s, as the reference does (Linux). `ldd` runs in its own process group,
  and if it still runs at 5 s the whole group is killed. Output written before that
  kill is dropped, so the marker decides. No log line is written, and the facts
  line keeps its shape. The bound applies on a fresh install and on a cache hit.
- If `ldd` exits before 5 s while a child still holds its output pipe, the probe
  waits up to 2 s more. Then it uses the output it has, even past 5 s, and the
  child is not killed (Linux).

### `__INSTALL_RESULT__` facts

```jsonc
{
  "serverVersion": "<daemon id>",
  "os":   "linux",            // GOOS
  "arch": "amd64",            // GOARCH
  "libc": "glibc",            // or "musl"; "" off linux (no probe). See the libc probe above.
                              // The driver uses this field to pick which CLI build to
                              // download, a third-binary claim (Driver claims in ARCHITECTURE.md).
  "cliPath": "<cli-dir>/<cli-version>", // <cli-version>.exe on Windows. It is "" when the
                              // cli-dir cannot be made, and without -cli-version
  "cliWasPresent": false,     // true only if it existed AND answered --version within 30 s.
                              // That is without CLAUDE_SSH_MANAGED_LAUNCHER=1; the gated rules
                              // are under the managed launcher above
  "cliError": "…",            // omitted on success
  "cliUnresponsive": true,    // a --version run that its bound stopped (bounds above).
                              // Omitted when it does not apply.
  // The launcher* fields appear only with CLAUDE_SSH_MANAGED_LAUNCHER=1,
  // each omitted when it does not apply. See the managed launcher above.
  "fetch": {                  // present whenever a -cli-url download was
    "bytes": 0,               // attempted, even a 0-byte 404. Omitted on -cli-zst / cache hit.
    "ms": 0,                  // download duration
    "longestPauseMs": 0       // largest gap between reads (~60000 on a read-idle stall abort)
  },                          // It is the last field without the gate.
  "launcherStatus": "usable", // none / usable / unusable / unreadable / probe_failed / unresponsive
  "launcher": ["<argv>"],     // the launcher argv, when one was resolved
  "launcherSource": "<file>", // the settings file that gave the value
  "launcherPath": "<file>",   // the unreadable file or folder
  "launcherReason": "…",      // why it was refused, failed or stopped
  "launcherStderr": "…"       // the failed run's stderr, 8192 bytes kept
}
```

## -probe-cli — classify a CLI binary

`-probe-cli <path>` runs the bounded `<path> --version` runnability probe and exits
`0`. It is how Claude Desktop classifies a CLI binary out of band, without a full
`-install`. Its stdout is:

- If the CLI runs and exits 0 within the bound, stdout stays empty.
- If the deadline had to kill it, stdout is `__CLI_HUNG__\n`.
- If the binary is missing or does not run, stdout is `__CLI_BAD__\n`. Not running
  means it either fails to start or exits non-zero.
- On Windows, a path with no file at exactly that name gives `__CLI_BAD__\n`, and
  nothing starts. The probe never adds `.exe`. claustrum tests only that the path
  exists. The exact test of the reference, a folder, a relative path and a path
  with another extension are not measured.

With `CLAUDE_SSH_MANAGED_LAUNCHER=1` the probe uses the
[managed launcher](protocol/launcher-resolve.md) (measured on Linux, unless a rule names its system). Every
`-probe-cli` row exits `0`, and each stderr text below ends in one newline:

- A usable launcher runs `<launcher argv...> <path> --version`. If the run exits
  0, stdout and stderr stay empty.
- An unusable launcher prints `__CLI_LAUNCHER__\n`, and the CLI does not run.
  stderr is `claude-ssh: <path> was not run: managed launcher unusable: <reason>`.
- Unreadable settings print `__CLI_LAUNCHER__\n`, and the CLI does not run. stderr
  is `claude-ssh: <path> was not run: managed settings unreadable: <file>: <reason>`.
- A failed launcher run prints `__CLI_LAUNCHER__\n`, not `__CLI_BAD__`. stderr
  holds four lines. The launcher output `L5: to stderr\n` is followed by one more
  newline:

```text
claude-ssh: the managed launcher's run printed:
L5: to stderr

claude-ssh: <path> --version through the managed launcher <argv0> did not succeed: exit status 3
```

- A launcher run that has not ended at 33 s is stopped. stdout is
  `__CLI_HUNG__\n`, and stderr is `claude-ssh: ` and the `cli unresponsive: …`
  text of `-install`.
- Without the gate the probe runs directly. On Windows the gate changes nothing.
- claustrum's choices (not measured) follow. A none answer runs the probe
  directly. An empty launcher stderr leaves out the `printed:` block. The stderr
  cap of `-install` applies. A signal reads `terminated by SIG<name>`.

The `-help` text of `-probe-cli` names this mode. With the managed launcher, the
reference changed this one `-help` line from `f6010b97` and no other (measured
on Linux, macOS and Windows).

The direct bound is a fixed 30 s, always applied. The mode unsets
`CLAUDE_RPC_TOKEN` so the probed child never inherits it. The probed CLI runs in
its own process group, so a terminal Ctrl-C does not reach it. On Unix the
fixed deadline group-kills the whole subtree, so a `--version` that forks a descendant
cannot outlive the probe. On Windows the direct child is killed and `WaitDelay` bounds
the mode, so a stray descendant is left to exit on its own. The mode installs no SIGINT
handler. A Ctrl-C therefore terminates the claustrum process itself, with exit 130 and
empty stdout. The mode ignores
SIGPIPE, as `-install` does, so a stdout pipe the caller closes mid-write fails the
write with EPIPE instead of terminating the process.
