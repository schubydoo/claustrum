# server.*

The `server.*` methods describe the daemon and stop it. `server.ping` tells a client that the daemon answers. `server.capabilities` lists the methods and the optional features of this build. `server.shutdown` stops the daemon. The surprise: `server.shutdown` needs no token, and its reply can lose a race with the close of the connection.

An untagged rule on this page matches build `5fd08069`, measured on Linux. A rule that names macOS or Windows was measured there too. The measured cells behind each rule are in the tests and in the [reference build history](../REFERENCE-BUILDS.md).

## Request

None of the three methods takes parameters. The daemon does not read `params` for them. A `params` of any type, or no `params` member, gives the same reply (from the code).

```json
{"jsonrpc": "2.0", "id": 1, "method": "server.ping", "auth": "<token>"}
```

`server.ping` and `server.capabilities` need the `auth` member, as every other method does. `server.shutdown` is the one method that is not authenticated. See [server.shutdown](#servershutdown).

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
  "features": ["process.stdin.offset", "files.extract_tar.execBit", "git.status.baseRepo", "git.info.discovered_root", "git.worktree_create.timeoutMs", "git.worktree_create.existingBranch", "git.worktree_remove.unpushedGuard", "process.spawn.shellAgentSocket", "launcher.managed", "server.peer_check", "git.shadow_dir", "git.worktree.external_root", "server.instance_id"],
  "peerCheck": "off"
}
```

The members come in this order:

| Member | Meaning |
|---|---|
| `version` | The version of the daemon. |
| `methods` | The 20 methods, in the order of [Methods](../PROTOCOL.md#methods-20). `plugins.prune` is last on every system. |
| `instanceId` | 32 hexadecimal characters, new at each start of the daemon. claustrum makes it from 16 random bytes (claustrum's own). On Linux and macOS the owner record of `daemon.lock` holds the same value. The banner of the [daemon log](../DAEMON.md#daemon-log-remote-serverlog) holds it too (measured on `89cb6289`). |
| `startedAt` | The start time of the daemon in unix milliseconds. claustrum sets it after its start steps and the bind of the socket (from the code). |
| `features` | The optional extensions that this build supports. See [Features](#features). |
| `peerCheck` | The state of the peer check. See [The peer check](#the-peer-check). |
| `peerCheckBy` | The parts that the check compares. With `"off"` or `"unavailable"` the reply has no such member. |

A request for `server.capabilities` does not count as activity for the idle exit of the daemon. Every other request past the auth step counts. The idle exit runs on Linux and macOS only. These three rules are claustrum's own, not measured. See [Orphan-exit self-probe](../DAEMON.md#orphan-exit-self-probe).

### Features

The `features` array is always present. The example above shows its members in order, as Linux and macOS send them. Windows sends no `git.worktree.external_root`. `server.instance_id` is always last.

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

The handler starts the stop and returns the reply at once. It does not wait for the connections to close. So the reply write races the close of the connection.

- If the reply wins, the client reads `{"jsonrpc":"2.0","id":<id>,"result":{"ok":true}}` and then EOF.
- If the close wins, the client reads EOF with no bytes.
- Another connection that is idle gets a clean EOF with no bytes.

A client must treat EOF after a shutdown request as a success. The reference often closes with no reply. claustrum sent the reply in each measured run. If both send it, the frame bytes are equal.

### What the stop does

The stop closes the listener. On Linux and macOS, if the socket file is still the one that the daemon bound, the stop removes it (from the code on macOS). On Windows the close of the listener removes the path, also the socket of a successor. Then the stop closes every connection and ends the children. That is the whole process group on Linux and macOS, and the direct child on Windows. With `-keep-children` the children keep running ([CT-2](../DIVERGENCES.md#ct-2)). The stop removes `daemon.token` by the rules of [Token persistence](../DAEMON.md#token-persistence-daemontoken). The daemon then exits with code 0 (from the code).

### Log lines

An untagged rule in this section is measured against `5fd08069` on Linux, unless the rule names another build or a system. The order of the shutdown lines and the signal lines were measured against `f6010b97` too.

On `server.shutdown` the daemon logs these two lines before it closes any connection:

```
[ServerHandler] server.shutdown received over RPC
[Server] shutdown requested
```

If the daemon logs a `[Server] Connection closed: <addr>` line, the line comes after the close of its connection. Then the daemon logs:

```
[Server] cleanup: closed <n> connection(s), killed <n> child process group(s)
```

The first count includes the connection that sent the request (measured against `5fd08069` on Windows only).

A stop by `SIGTERM` first logs `[daemon] received terminated; shutting down (children will be killed)`. Then it logs `[Server] shutdown requested` and the `cleanup:` line. For `SIGINT` the first line names that signal (measured against `5fd08069` on Linux and macOS). With `-keep-children` the first line says `kept` in place of `killed`, and the `cleanup:` line says `killed 0 child process group(s)` (both claustrum's own).

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

`-keep-children` is claustrum's own flag ([CT-2](../DIVERGENCES.md#ct-2)). The peer check follows the reference.
