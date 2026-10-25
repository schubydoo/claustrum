# claustrum

claustrum is a tiny, dependency-light Go daemon. It reimplements, clean-room, the small daemon that hosts a remote Claude Code session over SSH. Wire-level [probes](GLOSSARY.md#probe) of the [reference](GLOSSARY.md#reference) binary gave the contract. The project copied no code (see [`NOTICE`](https://github.com/schubydoo/claustrum/blob/main/NOTICE)) and sends no telemetry. One binary holds a local CLI-version manager, a process supervisor, and a JSON-RPC multiplexer with a replay buffer over an `AF_UNIX` socket. The daemon supplies 20 methods across the `server.*`, `files.*`, `git.*`, `launcher.*`, `process.*`, and `plugins.*` namespaces.

!!! note "The one hard rule"
    Stay byte-identical to the JSON-RPC frames of the reference daemon. The
    [wire](GLOSSARY.md#wire) surface *is* the product.

New here? Read the [Tour of one session](TOUR.md). It shows the frames of one session, from connect to shutdown. Then pick the path that fits you. A flag selects the mode of the binary, and the [flags table](PROTOCOL.md#flags-and-config-keys) lists each one.

<div class="grid cards" markdown>

- :material-api: **You write a client**
    1. [How claustrum works](ARCHITECTURE.md). The three runtime roles and the replay model.
    2. [Protocol overview](PROTOCOL.md). The transport, the error texts and the stream frames.
    3. [Worked client sessions](EXAMPLES.md). Runnable requests and a small client.
    4. [process.spawn](protocol/process-spawn.md), [process.reattach](protocol/process-reattach.md), and the other method pages under Methods in the left menu.

- :material-cog: **You run the daemon**
    1. [Running the daemon](DAEMON.md). The token, the lock, the log and the stop.
    2. [-install and -probe-cli](INSTALL.md). The install of the agent CLI.
    3. The [security policy](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md). `process.spawn` runs commands as the user of the daemon, so treat the socket and the token as shell access.

- :material-source-branch: **You contribute**
    1. [Divergences](DIVERGENCES.md). Every deliberate [divergence](GLOSSARY.md#divergence) from the reference.
    2. [Docs style guide](STYLE.md). Page templates and limits.
    3. The [contributing guide](https://github.com/schubydoo/claustrum/blob/main/CONTRIBUTING.md).

- :material-history: **You track the reference**
    1. [Upstream tracking](UPSTREAM-TRACKING.md). How the project keeps compatibility.
    2. [Reference builds](REFERENCE-BUILDS.md). The reference builds and their wire changes.
    3. [Shipped ledger](IMPROVEMENTS.md). The completed hardening work.

</div>

The [Glossary](GLOSSARY.md) defines the project terms.
