# GitHub Copilot app extension

This directory is the Dash0 plugin for the **GitHub Copilot app**, the desktop
app. It is separate from the Copilot **CLI** plugin in [`copilot/`](../copilot/README.md).
The app does not load CLI plugins or their hooks. It loads *session
extensions*: a folder with an `extension.mjs` that joins each session through
the bundled `@github/copilot-sdk`. No manifest is needed.

| File | Purpose |
|---|---|
| `extension.mjs` | Entry point. Joins the session, buffers its events per turn, and hands them to the binary |
| `copilot-app-on-event.sh` / `.ps1` | Shared bootstrap: downloads the `copilot-app-on-event` binary and verifies its checksum. Only the `.sh` one honours `DASH0_VERSION` |
| `skills/dash0-configure/` | Configure skill, which writes the config file |

## Install

Ask Copilot in the app:

> Install the extension at https://github.com/dash0hq/dash0-agent-plugin/tree/main/copilot-app

That runs `install_extension`, which copies this folder to
`~/.copilot/extensions/copilot-app/`, so it covers every session. To cover one
repository only, commit the folder as `.github/extensions/dash0-agent-plugin/`.
Either way, the extension loads in new sessions.

To uninstall, delete the folder.

## Configure

Run `/dash0-configure` in a session, or write the file yourself:

```markdown
---
otlp_url: "https://ingress.<region>.aws.dash0.com"
auth_token: "auth_..."
dataset: "default"
---
```

The file is `~/.copilot/dash0-agent-plugin.local.md`, or
`.copilot/dash0-agent-plugin.local.md` in a project. A project file replaces
the user file entirely, and the two never merge. The Copilot CLI plugin reads
the same file, so one setup covers both. Set `agent_name` if you want the two
told apart beyond `gen_ai.harness.name`.

**Put the token in the file.** The app removes secret-looking variables (`*_TOKEN`
and the like) from an extension's environment, so `DASH0_AUTH_TOKEN` exported in
a shell never reaches the extension. Every key in
[FEATURE_MATRIX.md](../FEATURE_MATRIX.md#configuration-options) is supported.

## How it works

```
Copilot app session ─ session events ─▶ extension.mjs ─ one call per turn ─▶ copilot-app-on-event ─▶ internal/pipeline ─▶ Dash0
                      (live stream)      buffers a turn                       internal/source/copilotapp
```

The extension makes four kinds of call to the binary. Each payload is JSON on
stdin, as with every other runtime:

| Call | When |
|---|---|
| `sessionStart` | when the extension joins (a repeated one does nothing) |
| `userPromptSubmitted` | when the main agent receives a `user.message` |
| `turnEnd` | at `session.idle`, or at the next prompt or the session's end if idle never came. Carries the turn's buffered events |
| `sessionEnd` | at `session.shutdown`, or when the `sessionEnd` hook reports `user_exit` |

Everything quantitative comes from the session events, so unlike the CLI there
is no native-OTel file and no launch function:

- **Tokens and model:** `assistant.usage`, one per model round. The main agent's
  rounds are summed into the turn's `chat` span, and each sub-agent's into its
  own `invoke_agent` span, at its own model. Summing a trace counts every token
  once.
- **Response:** the main agent's last `assistant.message`.
- **Tool spans:** `tool.execution_start` and `tool.execution_complete`, with
  their real timings, arguments, results, and MCP server.
- **Sub-agents:** `subagent.started` and `subagent.completed` become an
  `invoke_agent` span under the `task` tool span that spawned it. Its tools nest
  beneath it, matched by `parentToolCallId`. The span and the sub-agent's tool
  spans carry the sub-agent's name and its own model.
- **Failed turns:** a turn that ends on a `session.error`, or that the user
  aborted (`abort`, or `session.idle` with `aborted`), gives a failed `chat`
  span with the error. With `omit_io` on, only the error's category goes out,
  because the message can quote the request.
- **Steering:** a `user.message` with `delivery: "steering"` joins the running
  turn. Its text is added to the turn's input instead of starting a new turn.

The tree matches the other runtimes:
`chat → execute_tool task → invoke_agent explore → execute_tool view`.

### Behaviour of the app that shaped the design

All of these were checked against the SDK bundled with the app and against
recorded sessions:

- **`assistant.turn_end` is one model round, not one user turn.** The user turn
  ends at `session.idle`.
- **The `sessionEnd` hook fires after every prompt**, with reason `complete`.
  Only `user_exit` and `session.shutdown` end the session.
- **The extension starts with the first prompt, not with the session**, so that
  prompt is already in the history when the extension starts listening. The
  extension rebuilds the turn in progress from `session.getEvents()`. Live
  events that arrive during that read wait, and are handled in order once the
  turn is rebuilt. The history overlaps them, so only the history before the
  first of them is used. A first turn whose request fails at once
  (an unsupported model, a quota) can be over before the extension listens.
  It is still reported, with its error, unless the session was resumed.
  `assistant.usage` is not kept in the history, so the tokens that first turn
  spent before the extension listened come from the session's usage metrics
  (`usage.getMetrics()`), less what arrived live before the metrics were
  read. A turn with no usage at all
  takes its model from `assistant.message` and carries no token counts.
- **Sub-agent hooks fire into the parent's extension** under the sub-agent's own
  session id. The extension ignores them. A sub-agent's events carry an
  `agentId`, and the main agent's carry none.
- **stdout is the extension's JSON-RPC channel.** Nothing may write to it. The
  `dash0: connected` line goes to the session timeline through `session.log`,
  and everything else goes to stderr, which ends up in the app's log.
- Every handler catches its own errors, and the binary always exits 0.
  Telemetry never interrupts a session.

## Build & run locally

1. Build the binary into the bootstrap's cache, so the bootstrap uses it instead
   of downloading:

   ```bash
   go build -o ~/.local/state/dash0-agent-plugin/copilot-app/bin/copilot-app-on-event-$(sed -n 's/^VERSION="\(.*\)"/\1/p' copilot-app/copilot-app-on-event.sh)-$(go env GOOS)-$(go env GOARCH) ./cmd/copilot-app-on-event
   ```

2. Copy this folder into a test worktree as
   `.github/extensions/dash0-agent-plugin/`, and add a
   `.copilot/dash0-agent-plugin.local.md` there with `debug: "true"` and a
   `debug_file`. Point `otlp_url` at `go run ./test/e2e/mock-otlp-server` to
   keep traffic local.
3. Start a **new** session on that worktree. An extension does not reload
   mid-session.

Tests:

```bash
go test ./internal/source/copilotapp/ ./cmd/copilot-app-on-event/ ./test/consistency/
```

The fixture `internal/source/copilotapp/testdata/turn_with_subagent.json` is
made of events recorded from a real session and trimmed to the keys the
extension forwards.

## Limitations

- **The binary ships from v0.1.29.** Until that release exists, the bootstrap
  has nothing to download and the extension stays silent. Use the local build
  above.
- **No line counts for edits.** The app's edit tools carry no `structuredPatch`.
- **A turn interrupted by quitting the app** may be lost if the app never
  delivers `session.idle` or `session.shutdown`.
