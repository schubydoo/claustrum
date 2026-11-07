# server.adoptEnv

`server.adoptEnv` sets the environment that new children start from. Build `c9125eb8` added it. Each rule of this page is measured against `c9125eb8` on Linux, macOS and Windows, unless the rule names its systems or says otherwise.

## Request

```json
{"env": ["A=1", "HOME=/home/u", "SSH_AUTH_SOCK=/tmp/agent"], "noLoginShell": true}
```

| Parameter | Required | Meaning |
|---|---|---|
| `env` | yes | A list of `NAME=value` strings. |
| `noLoginShell` | no | A bool. The daemon accepts it. With `true` the adopt lets no [new read](#a-new-read-of-the-login-shell-path) start (Linux and macOS). An earlier adopt still lets one start (claustrum's own, not measured). |
| `kept` | no | A list of names, at most 8192 (measured on Linux and macOS, claustrum's own on Windows). A kept daemon variable stays if `env` lacks it, except a session-bound name (claustrum's own, not measured). |

The daemon ignores other members, with no log line (Linux and Windows).

## Response

A good list gives `{"ok":true}`. A refused list gives `-32602` `Invalid params` and this line:

```text
[ServerHandler] server.adoptEnv: refused the environment that a new connection sent (<why>). New processes keep starting from the previous environment.
```

| `<why>` | The list |
|---|---|
| `it is empty` | has no entry. A `params` of `{}` (Linux and Windows) or `null` gives it too |
| `it has more than 8192 entries` | has 8193 entries or more |
| `an entry has no equals sign` | has an entry with no `=` |
| `an entry contains a NUL byte` | has an entry with a NUL |
| `one variable is 128 KiB or longer, more than Linux allows when it starts a program` | has an entry of 131072 bytes or more. The text names Linux on every system |
| `it is larger than 256 KiB in total` | has more than 262144 bytes in all |
| `it names more than 8192 variables to keep` | has a `kept` of 8193 entries (Linux and macOS) |

The edges are measured on Linux, except the total: 262132 bytes passed and 300009 failed. claustrum adds up the entry lengths. The tests of each entry come before the count, and a NUL comes before a missing `=`. claustrum's total edge and its other orders are its own. These shapes give `-32602` with no line: absent params, an `env` that is a string, and entries that are not strings (Linux).

## The new environment

After a good adopt, a new child of any connection starts from the sent entries, not from the environment of the daemon. The git of a `gitRead` spawn is the exception ([process.spawn gitRead](process-git-read.md#the-run)). The steps of [process.spawn](process-spawn.md) follow.

- The session-bound names reach only the children of the connection that sent them. A later adopt of that connection replaces them. A connection with no adopt keeps those of the daemon. They are `DISPLAY`, `WAYLAND_DISPLAY`, `XAUTHORITY`, `KRB5CCNAME`, `XDG_SEAT`, `XDG_VTNR`, `GPG_TTY`, `WSL_INTEROP` and the names that start with `SSH_`, `SSH2_` or `XDG_SESSION_`. A prefix name that no row showed is claustrum's own, not measured.
- `TMPDIR`, `TMP`, `TEMP` and `CLAUDE_CODE_TMPDIR` keep the daemon value. On Linux and macOS the daemon value takes its [test](process-spawn.md#a-temp-folder-that-takes-no-file-build-683cc6a9). With no daemon value the child gets none (claustrum's own, not measured).
- Windows names are not case-sensitive: `Tmp` and `Display` follow the same rules.
- A sent name that starts with `CLAUDE_SSH_` reaches no child (Linux, one name). The `CLAUDE_SSH_DAEMON_CHILD` of the daemon stays.
- On Linux and macOS the child gets the login-shell PATH after a good read, as in process.spawn. Before one the sent PATH stays (claustrum's own, not measured). On Windows a list with no PATH gives a child with no PATH.
- A later good adopt replaces the set. A refused one keeps it.

### HOME on Windows

On Windows a sent `HOME` reaches a child only if `kept` names it (c9125eb8, measured on Windows). The daemon adds no `COMSPEC`, `PATHEXT` or `PROMPT`. These rules are claustrum's own, not measured:

- The log line counts a sent `HOME`. Its count matched `c9125eb8` in one Windows row.
- A kept `HOME` with no sent `HOME` takes the daemon value.

### The log line

A good adopt logs one line. This is the line of a list `["B=2"]` after a list of nine entries, on Linux:

```text
[Server] New processes now start from the environment sent on connection "@": 2 variables for every connection, 0 for this connection only. Names added, removed or changed (at most 40): ["A" "B" "HOME" "LANG" "LD_PRELOAD" "PATH" "USER" "ZZ_ADOPT"]
```

Each count counts a name once (the second measured on Windows, claustrum's own on Linux and macOS). The first includes the entries that the daemon keeps. The second counts the session-bound names, which the list leaves out. The names come in name order, at most 40, and Windows gives them in upper case. The label is `"@"` on Linux and Windows and `""` on macOS.

## A new read of the login-shell PATH

On Linux and macOS, after a good read, a good adopt with no `"noLoginShell":true` lets the PATH be read again, also with the same PATH. The next spawn of any connection starts the read in the background. On Linux the login shell gets the PATH of the adopt. That spawn keeps the old PATH, and later spawns get the new one.

- A spawn 31 s after the start of the last read starts no read, and one 61 s after it does.
- A spawn with `"disableShellAgentSocket":true` starts no read.
- On Linux a failed read keeps the old PATH, and its log line says `until one works, programs keep the last PATH that was read, or sshd's if none was`.
- After 24 reads again, Linux logs this line:

```text
[shellenv] Since 2026-11-07T11:00:36Z the login shell's PATH has been read again 24 times, each time because a new connection sent its environment. It is not read again before 2026-11-08T11:00:36Z; until then programs keep the PATH just read
```

### Rules not measured

These rules of the new read are claustrum's own, not measured:

- The exact 60 s, and no read after the cap line before the time it names.
- Only good reads count toward the cap. The window of 24 h starts at the first read of the daemon, and then a new window starts.
- A spawn inside the 60 s, or one that finds another login shell running, leaves the read due.
- After a later adopt with `"noLoginShell":true` the shell gets the PATH of the earlier adopt.
- With no PATH in the adopt the shell gets the daemon PATH.
- A `gitRead` spawn starts no read.
- After a failed read a later spawn tries again, and the pause doubles. After 16 failures in a row no read starts, also after a new adopt.
- An adopt before the first good read counts too.
- The cap line is at INFO level. A 24th read that ends after its window logs no cap line.
- On macOS the shell gets the PATH of the adopt, and the failure note and the cap line are those of Linux.

## The frame of the bridge

With `CLAUDE_SSH_ADOPT_ENV=1` the [bridge](flags.md#-bridge-stdiosocket-relay) first sends this method with its own environment in its own order. Windows sends the `=C:` entry first. The value `no-login-shell` adds `"noLoginShell":true` last. No variable and `yes` send no frame (Linux), nor do `true`, `0` and an empty value (macOS). The arg `no-login-shell` changes nothing (macOS). The `auth` member is the content of the [token file](../DAEMON.md#token-persistence-daemontoken) beside the socket (Linux, macOS and Windows):

```json
{"auth":"<token>","id":"bridge","jsonrpc":"2.0","method":"server.adoptEnv","params":{"env":["PATH=/usr/bin:/bin","HOME=/home/u","CLAUDE_SSH_ADOPT_ENV=1"]}}
```

The bridge does not relay the reply, and an error reply does not stop the relay (Linux). With no reply in about 1 s, or after a close by the daemon, the bridge connects again at once. The second connection gets the frame again. The third gets none and relays stdin (Linux, macOS and Windows). The relay of stdin starts after the reply (Linux). The wait covers the write of the frame too. A failed or late write counts as a close, any second failed try leads to the third connection, and a failed new connection ends the bridge with `dial server:` (claustrum's own, not measured).

## The list of the frame

The bridge does not test the list as the daemon does. 9000 entries go (Linux, macOS and Windows), and so does one entry of 131072 bytes (macOS). A frame over 262144 bytes, not counting the newline, is not sent (Linux and macOS). An entry that is not valid UTF-8 leaves `env`, and its name goes into `kept` after `env` (Linux and macOS):

```json
{"auth":"<token>","id":"bridge","jsonrpc":"2.0","method":"server.adoptEnv","params":{"env":["PATH=/usr/bin:/bin","CLAUDE_SSH_ADOPT_ENV=1"],"kept":["K"]}}
```

On Windows the environment is UTF-16, and such a value stays in `env` with U+FFFD. With no `daemon.token` the bridge sends no frame, and the list holds no `CLAUDE_RPC_TOKEN` (Linux, macOS and Windows). These parts are claustrum's own, not measured:

- An empty `daemon.token` gives no frame.
- The limit of 262144 bytes holds on Windows too.
- A name that is not valid UTF-8 goes into `kept` with U+FFFD for its bad bytes, and `kept` comes before `noLoginShell`.
