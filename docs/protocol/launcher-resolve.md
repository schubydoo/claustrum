# launcher.resolve

`launcher.resolve` tells a client which managed launcher the host administrator set for a CLI. A managed launcher is a program that runs the Claude Code CLI on this host. The method reads the managed settings files and answers with the launcher argv, or with the reason why there is none. It never runs the launcher, and it changes nothing on disk.

A client passes a usable `argv` to [process.spawn](../PROTOCOL.md#processspawn) in its `launcher` param. A spawn does not read the managed settings itself. The [launcher.* section](../PROTOCOL.md#launcher-added-89cb6289) of PROTOCOL.md has the other surfaces that use the launcher.

## Request

```json
{"cliPath": "/home/u/.claude/remote/ccd-cli/2.1.300"}
```

| Parameter | Required | Meaning |
|---|---|---|
| `cliPath` | yes | The path of the CLI that the launcher is to run. Any non-empty string is accepted, a relative one too. The daemon does not `~`-expand it (claustrum's own, not measured). |

The params errors are all `-32602`:

| Request | Error |
|---|---|
| No `params` member, a `params` that is not an object, or a `cliPath` that is not a string | `Invalid params` |
| `{}`, `"params":null`, a `null` or empty `cliPath`, or a misspelled key | `cliPath is required` |

Another method of the `launcher.*` namespace gets `-32601 Unknown method: launcher.<x>`.

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

The daemon reads the files on every call, so an edit shows on the next call. The `CLAUDE_SSH_MANAGED_LAUNCHER` variable does not change a frame.

The bytes follow Go's default JSON encoder. In a frame, `<`, `&`, `>` and `"` come out as JSON escapes, and so does U+2028 in an `argv`. U+0085, U+FFFD and the dash U+2014 of the reason texts stay raw. That dash is the bytes `e2 80 94`.

## The settings files

The settings folder is `/etc/claude-code` on Linux and `/Library/Application Support/ClaudeCode` on macOS. It holds the base file `managed-settings.json` and the drop-in folder `managed-settings.d`.

- A missing or empty folder answers `none`. So does a base file of `{}`, of zero bytes or of white space only.
- The base file is read first, then the drop-ins in name order (byte order, claustrum's own). A later file with a value wins, and `source` names that file.
- A drop-in name ends in `.json` in lower case and does not start with a dot. So `.hidden.json`, `x.JSON` and `a.js` are skipped. A folder entry and a FIFO entry are skipped, and the FIFO is not opened.
- A symbolic link entry is followed, and `source` keeps the link path. A dangling link is skipped. A link to a folder makes the answer unreadable.
- A regular file named `managed-settings.d` is ignored.
- A value of the wrong type in a later file is skipped, and the earlier value stays.
- One unreadable file makes the whole answer unreadable, also with a valid base file. The first unreadable file in read order is the one reported (claustrum's own).
- A UTF-8 byte order mark is dropped. A UTF-16LE mark with UTF-16LE text is read. An invalid byte in the file comes out as U+FFFD.

### The test variable

A non-empty `CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR` in the daemon's environment names another folder. The daemon reads it only if the system folder has neither the base file nor a `.json` drop-in, a hidden one included. Else it logs this line once:

```text
[launcher] CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR ignored: <system folder> holds this host's policy
```

### Unreadable reasons

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

## The value

Two string keys carry the value: `env.CLAUDE_CODE_PROCESS_WRAPPER` and `processWrapper`. In one file the env key wins. An env value of `""` counts as unset, so `processWrapper` answers. A `processWrapper` of `""` counts as unset too (claustrum's own). Each file gives one value, so a later file's `processWrapper` wins over an earlier file's env key (claustrum's own).

A value that starts with `[`, after leading white space, is a JSON array of strings. JSON escapes decode, and the metacharacter test does not apply.

Any other value is an argv list, not a shell command:

- A space, U+00A0 and U+FEFF split words. U+0085 does not. The other white space of Go's `unicode.IsSpace` splits words too (claustrum's own).
- Double quotes group words, so a quoted metacharacter is fine. Inside double quotes, `\"` gives `"` and `\\` gives `\`. Any other backslash stays, so `"e\nf"` keeps its two characters.
- Outside double quotes, a backslash is a plain character, and a `"` after it opens a quote. Single quotes are plain characters.

## Unusable reasons

The log line writes these texts raw. The frame escapes them as above.

| Case | `reason` |
|---|---|
| No launcher: three spaces, `""`, `[]` or `[""]` | `the value is set but contains no launcher — unset it to run without one, or set it to the absolute path of your launcher` |
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

## The launcher checks

`process.spawn` runs these same checks, with its command in place of `cliPath`.

- The checks test argv[0] only. The launcher arguments are not tested against files.
- The own-path check is a string compare. It comes before the absolute check and the existence check. So `c` with the `cliPath` `c` gets the own-path text, and so does a missing `/opt/claude/cli` with that `cliPath`.
- The script check comes before the existence check, so a missing `x.js` gets the script text (claustrum's own).
- A symbolic link to an executable file is usable, and `argv` keeps the link path. argv[0] is quoted as parsed, not cleaned or resolved.
- A long argv[0] is cut in the text at 256 bytes, back to the start of a character, and `…` follows inside the backticks. The log line shows the same cut. `argv` keeps the whole path. The exact limit is claustrum's own.

## Log lines

An unusable or unreadable answer writes one line at WARN. A usable or none answer writes none.

```text
[LauncherHandler] managed launcher from <source> refused: <reason>
[LauncherHandler] managed settings unreadable: <path>: <reason>
```

A repeat of the same line writes nothing. A usable or none answer between two equal refusals makes the second one log again. A `process.spawn` refusal writes no `[LauncherHandler]` line.

## Differences by system

| | Linux | macOS | Windows |
|---|---|---|---|
| Settings folder | `/etc/claude-code` | `/Library/Application Support/ClaudeCode` | None. The daemon reads no file and answers `none`. |
| `CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR` | Honored as above. | Honored as above. | Ignored. The answer is `none`. |

## Differences from the reference

No entry of the [divergence catalog](../DIVERGENCES.md) applies to this method. A rule marked "claustrum's own" is not measured on the reference.

The measured cells behind each rule are in the tests and in the [reference build history](../REFERENCE-BUILDS.md).
