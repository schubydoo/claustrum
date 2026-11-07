# launcher.resolve

`launcher.resolve` tells a client which managed launcher the host administrator set for a CLI. A managed launcher is a program that runs the Claude Code CLI on this host. The method reads the managed settings files and answers with the launcher argv, or with the reason why there is none. It never runs the launcher, and it changes nothing on disk.

A client passes a usable `argv` to [process.spawn](process-spawn.md#the-managed-launcher) in its `launcher` param. A spawn does not read the managed settings itself.

An untagged rule on this page matches build `89cb6289`, measured on Linux. A rule that names macOS or Windows was measured there. A (claustrum's own) rule is not measured on the reference.

## Request

```json
{"cliPath": "/home/u/.claude/remote/ccd-cli/2.1.300"}
```

| Parameter | Required | Meaning |
|---|---|---|
| `cliPath` | yes | The path of the CLI that the launcher is to run. Any non-empty string is accepted, a relative one too. The daemon does not `~`-expand it (claustrum's own) (not measured). |
| `path` | no | A launcher to check, with `launcherCheck` (`c9125eb8`). |
| `launcherCheck` | no | An object: `sha256`, a list of digests, and `ownerUid`, a uid (`c9125eb8`). See [The file check](#the-file-check-build-c9125eb8). |

## Response

The result has one of four shapes. Each keeps this member order:

```jsonc
{"status":"none"}
{"status":"usable","argv":["<launcher>","<arg>",…],"source":"<file>"}
{"status":"unusable","source":"<file>","reason":"<text>"}
{"status":"unreadable","reason":"<text>","path":"<file or folder>"}
```

| Status | Meaning |
|---|---|
| `none` | No settings file sets a launcher. |
| `usable` | `argv` is the launcher and its arguments. `source` is the settings file that set the value. |
| `unusable` | A value is set, but the daemon refuses it. `reason` says why. |
| `unreadable` | A settings file or the drop-in folder cannot be read. `path` names it. |

With `path` and `launcherCheck` the result has one of two shapes (`c9125eb8`, measured on Linux):

```jsonc
{"status":"usable","argv":["<path>"],"hostPolicySeen":<bool>}
{"status":"unusable","reason":"<text>","fault":"<fault>","hostPolicySeen":<bool>}
```

`argv` names `path`, also when the settings name another launcher. `hostPolicySeen` is `true` when a settings file or a drop-in holds a key other than `processWrapper`. It is `false` for `processWrapper` only, `{}`, no file (measured on Linux and macOS) and a file that is not JSON (macOS). A `launcherCheck` with no `path` on Linux and Windows gets one of the four shapes (claustrum's own, not measured).

The daemon reads the files on every call, so an edit shows on the next call. The `CLAUDE_SSH_MANAGED_LAUNCHER` variable does not change a frame (measured on macOS).

The bytes follow Go's default JSON encoder. In a frame, `<`, `&`, `>` and `"` come out as JSON escapes, and so does U+2028 in an `argv`. U+0085, U+FFFD and the dash U+2014 (bytes `e2 80 94`) of the reason texts stay raw.

## Errors

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | No `params` member, a `params` that is not an object, or a `cliPath` that is not a string. |
| `-32602` | `cliPath is required` | `{}`, `"params":null`, a `null` or empty `cliPath`, or a key with another name, such as `path` (measured on Windows). |
| `-32602` | `launcherCheck is required with path` | A `path` with no `launcherCheck` (`c9125eb8`). The error has no `data`. |
| `-32601` | `Unknown method: launcher.<x>` | Another method of the `launcher.*` namespace. |

## Behavior

With `CLAUDE_SSH_MANAGED_LAUNCHER=1`, [`-install`](../INSTALL.md#-install-ensure-the-agent-cli) and [`-probe-cli`](../INSTALL.md#-probe-cli-classify-a-cli-binary) run the CLI through the launcher too.

### The settings files

The settings folder ([Platform notes](#platform-notes)) holds the base file `managed-settings.json` and the drop-in folder `managed-settings.d`.

- A missing or empty folder answers `none`. So does a base file of `{}`, of zero bytes or of white space only.
- The base file is read first, then the drop-ins in name order (byte order) (claustrum's own). A later file with a value wins, and `source` names that file.
- A drop-in name ends in `.json` in lower case and does not start with a dot. So `.hidden.json`, `x.JSON` and `a.js` are skipped. A folder entry and a FIFO entry are skipped, and the FIFO is not opened.
- A symbolic link entry is followed, and `source` keeps the link path. A dangling link is skipped. A link to a folder makes the answer unreadable.
- A regular file named `managed-settings.d` is ignored.
- A value of the wrong type in a later file is skipped, and the earlier value stays (`b3081b51`, measured on Linux). With no earlier value, the refusal of the later file answers (`b3081b51`, measured on Linux). A later value or refusal replaces an earlier refusal (`b3081b51`, measured on Linux).
- One unreadable file makes the whole answer unreadable, also with a valid base file. The first unreadable file in read order is the one reported (claustrum's own).
- A UTF-8 byte order mark is dropped. A UTF-16LE mark with UTF-16LE text is read. An invalid byte in the file comes out as U+FFFD.

### The value

Two string keys carry the value: `env.CLAUDE_CODE_PROCESS_WRAPPER` and `processWrapper`. In one file the env key wins. An env value of `""` counts as unset, so `processWrapper` answers. A `processWrapper` of `""` counts as unset too (claustrum's own). Each file gives one value, so a later file's `processWrapper` wins over an earlier file's env key (claustrum's own).

A value that starts with `[`, after leading white space, is a JSON array of strings. JSON escapes decode, and the metacharacter test does not apply.

Any other value is an argv list, not a shell command:

- A space, U+00A0 and U+FEFF split words. U+0085 does not. The other white space of Go's `unicode.IsSpace` splits words too (claustrum's own).
- Double quotes group words, so a quoted metacharacter is fine. Inside double quotes, `\"` gives `"` and `\\` gives `\`. Any other backslash stays, so `"e\nf"` keeps its two characters.
- Outside double quotes, a backslash is a plain character, and a `"` after it opens a quote. Single quotes are plain characters.

### A value that is not a string

A `processWrapper` of a number is unusable, and so is an env value of a number (`b3081b51`, measured on Linux and macOS). A `processWrapper` of a boolean, an array or an object is unusable too (`b3081b51`, measured on Linux). An `env` of a string counts as unset (`b3081b51`, measured on Linux and macOS). A `processWrapper` of `null` counts as unset (`b3081b51`, measured on Linux). A string env value wins over a `processWrapper` of a number (`b3081b51`, measured on Linux). An env value of `null`, a boolean, an array or an object is unusable (`b3081b51`, measured on Linux). A string `processWrapper` wins over an env value of a number or a boolean (`b3081b51`, measured on Linux). If the env value and the `processWrapper` are both refused, or the `processWrapper` is `""`, the env refusal answers (`b3081b51`, measured on Linux).

The `reason` is `<key> must be one string (the launcher and its arguments), not <kind>`. `<key>` is `processWrapper` or `env.CLAUDE_CODE_PROCESS_WRAPPER`. `<kind>` is `null`, `a number`, `a boolean`, `an array` or `an object` (`b3081b51`, measured on Linux).

### Unusable reasons

The log line writes these texts raw. The frame escapes them as in [Response](#response).

| Case | `reason` |
|---|---|
| No launcher: three spaces, a value of two double quotes, `[]` or `[""]` | `the value is set but contains no launcher — unset it to run without one, or set it to the absolute path of your launcher` |
| A later empty JSON element, `["<w>",""]` | `the JSON array contains an empty element — remove it, or fill in the value it was a placeholder for` |
| A JSON element that is not a string | `JSON form must be an array of strings` |
| Starts with `[` and is not valid JSON | ``value starts with `[` but is not valid JSON`` |
| An unquoted `;` `\|` `&` `$` `(` `)` backtick `<` `>` | ``the value contains an unquoted shell metacharacter (one of ; \| & $ ( ) ` < >) — it is an argv list, not a shell command`` |
| An open double quote | `unterminated double quote` |
| A later empty `""` word | ``the value contains an empty `""` token — remove it, or fill in the value it was a placeholder for`` |
| argv[0] is not absolute | `the launcher must be an absolute path, not a bare name resolved via PATH` |
| argv[0] equals `cliPath` | ``launcher `<argv0>` is Claude Code's own path`` |
| argv[0] ends in `.js`, `.mjs`, `.ts`, `.tsx` or `.jsx` (case-sensitive) | ``launcher `<argv0>` is a script the SDK would run in place of Claude Code`` |
| argv[0] is missing, a folder or not executable | ``launcher `<argv0>` does not exist or is not an executable regular file`` |

### The launcher checks

`process.spawn` runs these same checks, with its command in place of `cliPath`, after [its own first steps](process-spawn.md#the-managed-launcher).

- The checks test argv[0] only. The launcher arguments are not tested against files.
- The own-path check is a string compare. It comes before the absolute check and the existence check. So `c` with the `cliPath` `c` gets the own-path text, and so does a missing `/opt/claude/cli` with that `cliPath`.
- The script check comes before the existence check, so a missing `x.js` gets the script text (claustrum's own).
- A symbolic link to an executable file is usable, and `argv` keeps the link path. argv[0] is quoted as parsed, not cleaned or resolved.
- A long argv[0] is cut in the text at 256 bytes, back to the start of a character, and `…` follows inside the backticks. The log line shows the same cut. `argv` keeps the whole path. The exact limit is claustrum's own.

### The file check (build `c9125eb8`)

Only Linux runs it ([Platform notes](#platform-notes)). It tests the file at `path`, or argv[0] of a [spawn](process-spawn.md#a-launcher-check-build-c9125eb8). `<L>` is that file and `<D>` the item at fault. The texts are measured on Linux.

#### Faults of the file

| `fault` | Case | `reason` |
|---|---|---|
| `not_absolute` | A relative path | `the launcher must be an absolute path, not a bare name resolved via PATH` |
| `script_suffix` | `.js`, `.ts` or `.mjs`, and `.tsx` or `.jsx` (claustrum's own). `X.JS` passes | ``launcher `<L>` ends like a JavaScript or TypeScript source file, and no launcher may. Rename it.`` |
| `missing` | Missing, a dangling link or a folder | ``launcher `<L>` does not exist or is not an executable regular file`` |
| `missing` | Mode 0644, or a mount with `noexec` | ``launcher `<L>` is there, but the account that signs in cannot run it (its mode, or a file system mounted noexec)`` |
| `unreadable` | The read fails | ``launcher `<L>` cannot be read for its digest: <errno text>`` |
| `unreadable` | More than 67108864 bytes | ``launcher `<L>` is larger than 64 MiB, the most that can be checked against a digest`` |
| `digest` | No digest of `sha256` matches | ``launcher `<L>` matches none of the SHA-256 digests in the policy. Run sha256sum on the host to see its digest.`` |

#### Faults of the walk

| `fault` | Case | `reason` |
|---|---|---|
| `writable` | A group or other write bit on the file or a folder above it, 1777 too | `` `<D>` can be changed by accounts other than its owner (mode <mode>), so the launcher can't be trusted. Remove that write access, or keep the launcher somewhere that has none. `` |
| `writable` | NFS, with no digest | `` `<D>` is on a file system (NFS) whose server decides who can change it, whatever its owner and mode show. Add the launcher's SHA-256 digest to sshHostLauncher as sha256. `` |
| `owner` | Owned by the uid of the daemon | `` `<D>` belongs to the account that signs in (uid <n>). Root, or another account named as ownerUid in the policy, must own the launcher and every folder above it. `` |
| `owner` | Owned by uid 65534 | `` `<D>` shows as uid 65534 (nobody), which usually means this login can't see its real owner, as in a sandbox that doesn't map root or on a squashed network export. This login must see root as the owner of the launcher and every folder above it. `` |
| `owner` | Owned by another uid | `` `<D>` belongs to uid <n>. Root must own the launcher and every folder above it. If you trust uid <n> with the launcher, you can name it as ownerUid in sshHostLauncher instead. `` |

#### Rules of the check

- `<mode>` has four octal digits. The group of the file is not tested.
- A root file passes with an `ownerUid` too. An `ownerUid` lets that uid own them (claustrum's own, not measured). An `ownerUid` in the settings file lifts nothing.
- With no digest, `{}` or `"sha256":[]`, the file is not read (claustrum's own, not measured). A digest matches in any letter case. A short one matches nothing.
- Links pass, a chain of 40 too. The walk tests `/`, each folder down to the file, and the file. It tests the folders of the path as written, then those after the links (claustrum's own).
- Each link met on the way to the launcher has its holder folder and the folders above it tested like the others (claustrum's own, not measured).
- A folder high in the path is refused before a lower one. The other order of the tests is claustrum's own. A change during the check is not guarded (claustrum's own).

### Log lines

An unusable or unreadable answer writes one line, at WARN (claustrum's own). A usable or none answer writes none.

```text
[LauncherHandler] managed launcher from <source> refused: <reason>
[LauncherHandler] managed settings unreadable: <path>: <reason>
```

A repeat of the same line writes nothing. A usable answer between two equal refusals makes the second one log again. A none answer does the same (claustrum's own). A `process.spawn` refusal writes no `[LauncherHandler]` line.

An unusable answer to a `launcherCheck` writes this line each time, a repeat too (`c9125eb8`, measured on Linux and macOS):

```text
[LauncherHandler] the launcher that the desktop names is refused: <reason>
```

## Platform notes

=== "Linux and macOS"

    The settings folder is `/etc/claude-code` on Linux and `/Library/Application Support/ClaudeCode` on macOS. `CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR` is honored as in [Rare states](#rare-states).

    On macOS each resolve with a `launcherCheck` answers unusable with the fault `macos_host`, before any file test. That holds with no `path` and with no settings file (`c9125eb8`). With a `path` it holds too (claustrum's own, not measured). The reason is `sshHostLauncher cannot be used with a macOS host: an access-control list there can let another account change the launcher without this file check seeing it.`

=== "Windows"

    There is no settings folder, so the daemon reads no file and answers `none`. `CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR` is not read, and no line is logged. A `path` with a `launcherCheck` answers unusable with the fault `windows_host` and the reason `a launcher cannot be applied on a Windows host` (claustrum's own, not measured).

## Rare states

??? note "The test variable"
    A non-empty `CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR` in the daemon's environment names another folder. If the system folder has neither the base file nor a `.json` drop-in, a hidden one included, the daemon reads the other folder. Otherwise it logs this line once per daemon. That no further line comes from the third resolve on is claustrum's own:

    ```text
    [launcher] CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR ignored: <system folder> holds this host's policy
    ```

??? note "Unreadable reasons"
    | The file | `reason` |
    |---|---|
    | Mode 000, daemon not root (Linux and macOS) | `permission denied` |
    | A folder | `is a directory` |
    | A FIFO (answered at once, not opened) | `not a regular file` |
    | More than 2097152 bytes | `larger than 2 MiB` |
    | `{` | `not valid JSON` |
    | `[]` or `null` | `not a JSON object` |
    | A UTF-16BE mark, or a raw U+00A0 before the JSON | `not valid JSON` |
    | A drop-in folder whose listing fails | `the drop-in directory could not be listed: permission denied`, with the folder as `path` |

    For a drop-in, the reasons other than `is a directory` and `not valid JSON` are claustrum's own.

## Differences from the reference

No entry of the [divergence catalog](../DIVERGENCES.md) applies to this method.

The measured cells behind each rule are in the tests and in the [reference build history](../REFERENCE-BUILDS.md).
