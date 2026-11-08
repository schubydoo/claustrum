# server.*

Three `server.*` methods describe the daemon and stop it. `server.ping` tells a client that the daemon answers. `server.capabilities` lists the methods and the optional features of this build. `server.shutdown` stops the daemon. [server.adoptEnv](server-adopt-env.md) has its own page. The surprise: `server.shutdown` needs no token, and its reply can lose a race with the close of the connection.

An untagged rule on this page matches build `5fd08069`, measured on Linux. A rule that names macOS or Windows was measured there too.

## Request

None of the three methods reads `params`. A `params` of any type, or no `params` member, gives the same reply (from the code).

```json
{"jsonrpc": "2.0", "id": 1, "method": "server.ping", "auth": "<token>"}
```

`server.ping` and `server.capabilities` need the `auth` member. `server.shutdown` is the one method that is not authenticated. See [server.shutdown](#servershutdown).

`server.version` does not exist. It answers `-32601 Unknown method: server.version`, as any unknown method does. The `-version` flag still prints the version of the daemon.

## server.ping

```json
{"pong": true}
```

The reply is always this result.

## server.capabilities

```json
{
  "version": "<id>",
  "methods": ["server.ping", "server.capabilities", "server.shutdown", "…"],
  "instanceId": "<32 hex characters>",
  "startedAt": 1760000000000,
  "features": ["process.stdin.offset", "process.kill.farewell", "files.extract_tar.execBit", "files.list.includeDotNames", "files.list.junctionsAsDirs", "git.status.baseRepo", "git.info.discovered_root", "git.worktree_create.timeoutMs", "git.worktree_create.existingBranch", "git.worktree_create.verified_config", "git.worktree_create.verify_error_codes", "git.worktree_remove.unpushedGuard", "process.spawn.shellAgentSocket", "process.spawn.failure_reason", "process.spawn.git_read", "server.line_end", "launcher.managed", "launcher.non_string_refused", "launcher.supplies_cli", "server.peer_check", "git.outside_session_folders", "git.shadow_dir", "git.hooks_null_device", "git.status.own_lfs_store", "files.read.real", "files.mkdir.verified_parent", "git.worktree.external_root", "process.spawn.link_bound", "process.spawn.cli_temp_root", "server.instance_id"],
  "peerCheck": "off"
}
```

The members come in this order:

| Member | Meaning |
|---|---|
| `version` | The version of the daemon. |
| `methods` | The 23 methods, in the order of [Methods](../PROTOCOL.md#methods-23). `files.realpath` is last on every system (`683cc6a9`). On Windows (`c9125eb8`, measured) there is no `files.mkdir`, so claustrum lists 22. |
| `instanceId` | 32 hexadecimal characters, new at each start of the daemon. claustrum makes it from 16 random bytes (claustrum's own). On Linux and macOS the owner record of `daemon.lock` holds the same value. The banner of the [daemon log](../DAEMON.md#daemon-log-remote-serverlog) holds it too (measured on `89cb6289`). |
| `startedAt` | The start time of the daemon in unix milliseconds. claustrum sets it after its start steps and the bind of the socket (from the code). |
| `features` | The optional extensions that this build supports. See [Features](#features). |
| `peerCheck` | The state of the peer check. See [The peer check](#the-peer-check). |
| `peerCheckBy` | The parts that the check compares. With `"off"` or `"unavailable"` the reply has no such member. |

A request for `server.capabilities` does not count as activity for the idle exit of the daemon. Every other request past the auth step counts. The idle exit runs on Linux and macOS only. These three rules are claustrum's own, not measured. See [Orphan-exit self-probe](../DAEMON.md#orphan-exit-self-probe).

### Features

The `features` array is always present. The example above shows the members in the order of `b3081b51` on Linux and macOS, less `process.spawn.pty`, plus `process.spawn.git_read` and `launcher.supplies_cli` of `c9125eb8`. They follow `process.spawn.failure_reason` and `launcher.non_string_refused` on Linux, macOS and Windows. `be1c1ca5` adds `git.hooks_null_device` after `git.shadow_dir` on Linux, macOS and Windows. `b3081b51` adds five names of the example on the three systems, and `process.spawn.cli_temp_root` on Linux and macOS only. `c9125eb8` adds `files.mkdir.verified_parent` after `files.read.real` on Linux and macOS only (measured), and the example holds that name too. Windows sends no `git.worktree.external_root` and no `process.spawn.link_bound` (`683cc6a9`), and with jobs on adds `process.spawn.job` last (`52d82f0a`). On Linux with a `/proc` that holds only `self`, `683cc6a9` drops `files.read.real`. claustrum drops it after a failed start test, whose exact steps are claustrum's own.

`CLAUDE_SSH_MANAGED_LAUNCHER` does not change the list (measured on macOS). Windows lists `launcher.managed` too, although a Windows `process.spawn` refuses every launcher. The peer check changes only the `peerCheck` and `peerCheckBy` members.

### The peer check

The peer check lets the daemon refuse a caller that runs in other namespaces, for example a process in a sandbox. Only the variable `CLAUDE_SSH_PEER_CHECK=1` in the environment of the daemon turns it on. The value must be exactly `1`. Any other value, for example `0`, `true`, `on`, `01` or the empty value, leaves it off. No variable leaves it off too.

The daemon reads the variable once, at its start, and then removes it from its own environment. On Windows the removal covers any letter case. So no child of the daemon inherits it: no `process.spawn` child, no git call, no launcher run and no login-shell read (the launcher run and the login-shell read: from the code). A `process.spawn` request whose `env` names the variable still gives it to that child. If the removal fails, the daemon logs one WARN line (claustrum's own), and the children inherit the variable.

The `peerCheck` member:

| Value | When |
|---|---|
| `"off"` | The variable is not `1`. The daemon serves every caller. |
| `"on"` | Linux, with the variable at `1`. The reply then ends with `"peerCheckBy":["network","pidfd"]`. |
| `"unavailable"` | macOS and Windows, with the variable at `1`. The daemon serves every caller. |

On a Linux kernel that lacks a part of the check, claustrum answers `"unavailable"`, `"network_only"` or `"by_pid"`. It can also answer `"on"` with another `peerCheckBy` (claustrum's own, not measured).

With `1` the daemon writes one line at its start (the level is claustrum's own). The texts are in [Daemon log](../DAEMON.md#daemon-log-remote-serverlog).

### A refused caller (Linux)

On Linux with the check on, the daemon checks each new connection before it reads from it. It compares the namespaces of the caller with its own. A caller that the check refuses gets one frame, and then the connection closes:

```
{"jsonrpc":"2.0","id":null,"error":{"code":-32006,"message":"Refused: this server could not confirm that the caller is in its own namespaces","data":{"reason":"other_namespace"}}}
```

| `reason` | When |
|---|---|
| `other_namespace` | The caller is in other namespaces. |
| `unreadable` | The daemon cannot read the namespace link of the caller. |
| `lookup_failed` | The caller exited before the check. claustrum gives every other failure of the lookup this reason too (not measured). |

The daemon reads no request from a refused connection. So an unauthenticated `server.shutdown` on that connection does nothing. A caller that wrote first reads the frame and then a reset. A refused connection gets no `New connection` log line. Refusals 1 to 10 each log one WARN line (the level is claustrum's own). Later refusals log none:

```
[Server] refused caller number 1 (other_namespace): "user:[4026532385] net:1 pidfd"; this server's namespaces are "user:[4026531837] net:1 pidfd"
```

A caller that exited before the check gets no line.

## server.shutdown

```json
{"ok": true}
```

`server.shutdown` stops the daemon. It is not authenticated: a shutdown frame stops the daemon with any `auth` member or none. The exemption covers auth only. A frame with a missing or wrong `jsonrpc` version gets `-32600 Invalid JSON-RPC version`, and the daemon stays up. See [Authentication](../PROTOCOL.md#authentication). `-stop` sends this method with no `auth` member.

### The reply races the close

The handler starts the stop and returns the reply at once. On Windows the reply first waits until the stop starts to close the connections, at most 500 ms (claustrum's own, not measured). So the reply write races the close.

- If the reply wins, the client reads `{"jsonrpc":"2.0","id":<id>,"result":{"ok":true}}` and then EOF.
- If the close wins, the client reads EOF with no bytes.
- Another connection that is idle gets a clean EOF with no bytes.

A client must treat EOF after a shutdown request as a success. In one round on Windows `b3081b51` replied in 0 of 60 plain runs and 17 of 60 after a ping (measured on Windows). With a second idle connection it replied in 2 of 60. If claustrum and `b3081b51` both send the reply, the frame bytes are equal.

### What the stop does

The stop closes the listener. On Linux and macOS, if the socket file is still the one that the daemon bound, the stop removes it (from the code on macOS). On Windows the close of the listener removes the path, also the socket of a successor. Then the stop closes every connection and ends the children. That is the whole process group on Linux and macOS, and the job of each child on Windows (`52d82f0a`), except [D28](../DIVERGENCES.md#d28).

On Linux and macOS `b3081b51` first sends `SIGTERM` to a child whose args hold `stream-json`, `--input-format=stream-json` or `--output-format=stream-json`, and to that child alone. With a launcher the args of the request decide (the last form and the launcher measured on Linux). Every other child gets the group `SIGKILL` at once. A child that stays gets the group `SIGKILL` 1.5 s after the `SIGTERM`. So the stop takes up to 1.5 s longer (measured on Linux and macOS). On Linux `b3081b51` also sends the group `SIGKILL` after the exit of an ended child (measured). claustrum sends none after the exit frame ([D28](../DIVERGENCES.md#d28)). On Windows every child ends at once by the kill of its job (`b3081b51`, measured on Windows).

With `-keep-children` the children keep running ([CT-2](../DIVERGENCES.md#ct-2)). The stop removes `daemon.token` by the rules of [Token persistence](../DAEMON.md#token-persistence-daemontoken). The daemon then exits with code 0 (from the code).

### Log lines

An untagged rule in this section is measured against `5fd08069` on Linux, unless the rule names another build or a system. The order of the shutdown lines and the signal lines were measured against `f6010b97` too.

On `server.shutdown` the daemon logs these two lines before it closes any connection:

```
[ServerHandler] server.shutdown received over RPC
[Server] shutdown requested
```

If the daemon logs a `[Server] Connection closed: <label>` line, the line comes after the close of its connection. Then the daemon logs:

```
[Server] cleanup: closed <n> connection(s), ended <n> child process group(s)
```

The first count includes the connection that sent the request (measured against `5fd08069` on Windows only). The word `ended` is that of `b3081b51`, where `be1c1ca5` says `killed` (measured on Linux, macOS and Windows). Before it, a stop that sent `SIGTERM` logs this line (`b3081b51`, measured on Linux and macOS):

```
[process.Manager] KillAll: asked <n> Claude Code process(es) to end: <n> exited, <n> had to be killed, after <duration>
```

A stop by `SIGTERM` first logs `[daemon] received terminated; shutting down (children will be killed)`. Then it logs `[Server] shutdown requested` and the `cleanup:` line. For `SIGINT` the first line names that signal (measured against `5fd08069` on Linux and macOS). With `-keep-children` the first line says `kept` in place of `killed`, and the `cleanup:` line says `ended 0 child process group(s)` (both claustrum's own).

A handler panic in `server.shutdown` writes no frame (claustrum's own). See [Handler panic recovery](../PROTOCOL.md#handler-panic-recovery).

## Differences by system

[The peer check](#the-peer-check) and [Features](#features) hold the other differences. The start line with `CLAUDE_SSH_PEER_CHECK=1` differs:

=== "Linux"
    INFO. It names the namespaces of the daemon (claustrum's own).

=== "macOS"
    WARN. It says that the system names no namespaces (claustrum's own).

=== "Windows"
    WARN, as on macOS (claustrum's own).

## Differences from the reference

`-keep-children` is claustrum's own flag ([CT-2](../DIVERGENCES.md#ct-2)). The peer check follows the reference. claustrum lacks the terminal of `52d82f0a`, of `683cc6a9`, of `be1c1ca5`, of `b3081b51` and of the pinned build `c9125eb8`. So it lists no `process.resize`, and on Linux and macOS no `process.spawn.pty` ([open items](../REFERENCE-BUILDS.md#c9125eb87bca9ab294d4190dd79be9dfb8d11806-2026-11-02-built)).
