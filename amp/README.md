# Amp CLI and Orbs

This native [Amp plugin](https://ampcode.com/docs/plugin-api) sends completed
turns and paired tool calls to Dash0. It works on the executor running Amp,
whether that is a local CLI or an Orb. It does not wrap the `amp` command.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/dash0hq/dash0-agent-plugin/main/install-amp.sh | bash
```

On Windows:

```powershell
irm https://raw.githubusercontent.com/dash0hq/dash0-agent-plugin/main/install-amp.ps1 | iex
```

The installer downloads the helper for this platform, verifies it against the
release checksums, and installs it as `amp-on-event` (`amp-on-event.exe` on
Windows) next to `index.ts` in `~/.config/amp/plugins/dash0/`. The bridge spawns
the helper by that exact name, so the release asset
`amp-on-event-<os>-<arch>` must be renamed on the way in — which is what the
installer is for. It then prompts for the OTLP endpoint, token, dataset, and
team, and writes them to `~/.amp/dash0-agent-plugin.local.md` (mode 600). Pass
`--endpoint`, `--token`, `--dataset`, and `--team` to skip the prompts, or set
`DASH0_OTLP_URL` / `DASH0_AUTH_TOKEN` / `DASH0_DATASET` / `DASH0_TEAM_NAME`.
Run `install-amp.sh --help` for the full list. No JavaScript dependencies are
needed at runtime; Amp runs `index.ts` with Bun and erases the type-only import.

Reload plugins from Amp's command palette. Install just this plugin once per
executor. To restrict installation to one workspace, run the installer with
`--project` (`-Project` on Windows), which uses `.amp/plugins/dash0/` in the
current directory. The credential file stays user-level either way; a workspace
`.amp/dash0-agent-plugin.local.md` outranks it if you add one, and must be kept
out of Git.

The installer asks the latest release for the helper, or the release pinned by
`DASH0_VERSION`. The helper is included in release builds from the release that
carries this integration onwards; it does not assume an already published
release contains it, and an older pin fails rather than installing something
that cannot run. Until then, use the from-source path below.

For an Orb, run the same Linux installation in the Orb, or add the installer to
the project's setup script. A local installation is not copied to an Orb.
User-local installations inside an Orb last only as long as that Orb. For new
Orbs, automate installation in project setup.

To uninstall, run `uninstall-amp.sh` (`uninstall-amp.ps1` on Windows), which
removes only the `dash0` plugin directory and the credential file. Reload
plugins afterwards.

### Install from source

For developing this plugin, or on a host with no network, build the helper and
point the installer at the working tree instead of a release:

```sh
go build -o amp/amp-on-event ./cmd/amp-on-event
DASH0_SOURCE_DIR=amp ./install-amp.sh
```

On Windows, use PowerShell:

```powershell
go build -o amp/amp-on-event.exe ./cmd/amp-on-event
$env:DASH0_SOURCE_DIR = 'amp'; .\install-amp.ps1
```

That copies `index.ts` and the helper you just built into the same plugin
directory, under the same names, and requires Go 1.25+.

Execute mode must wait for plugins to load:

```sh
amp --plugin-ready-timeout -x 'Your task'
```

For Orb execute mode, install in the remote project setup, then use
`amp --plugin-ready-timeout -ox 'Your task'`. The stream-json wrapper is not the
telemetry source. Verify hook delivery for the Amp version you deploy.

## Configure on the executor

Use `.amp/dash0-agent-plugin.local.md` in the workspace, or
`~/.amp/dash0-agent-plugin.local.md` for a user default. Keep it out of Git.

```yaml
---
enabled: true
otlp_url: https://ingress.YOUR_REGION.aws.dash0.com
auth_token: YOUR_DASH0_TOKEN
dataset: default
agent_name: amp
omit_user_info: true
omit_identity_fallback: true
---
```

The shared configuration rules apply: `AMP_PLUGIN_OPTION_<KEY>` overrides the
file, then ordinary options fall back to `DASH0_<KEY>`. Authentication never
uses the generic environment fallback. A configured macOS keychain token takes
precedence, as with other integrations. No `.env` file is loaded automatically.
Use your project's secret delivery mechanism for Orb credentials.

Prompt, response, and tool argument/output content follow the shared `omit_io`
rule, as on every other harness: **with the default `omit_io: true` every
content field is replaced by `<REDACTED>`**, and only `omit_io: false` sends the
real text. Thinking blocks, inline image payloads, and image URLs are dropped in
the bridge, so no exporter setting can turn those back on. Each content field is
capped at 16 KiB and a whole turn at 256 KiB, after which `dash0.amp.truncated`
is set and later fields carry no content. If the encoded envelope still passes
768 KiB — tool names and message IDs are outside the content budget, and JSON
escaping expands control-heavy text — the bridge drops every content field and
keeps the turn: a turn is never dropped to make room for its output. Standard VCS, team, and identity attributes still follow shared
exporter settings. No transcript or event log is written.
The shared `DEBUG`/`DEBUG_FILE` options write the outgoing OTLP request and HTTP
attempt outcomes to a debug file, so with `omit_io: false` that file holds the
prompt, response, and tool content in plain text at mode 0644. Do not set
`DEBUG_FILE` on a shared host while content capture is on.
Set `AMP_PLUGIN_OPTION_DEBUG=true` to also log the helper's exit
code, signal, and elapsed time in Amp. An HTTP success is not proof of storage:
the token must permit span ingestion into the configured dataset. Verify a
turn with a read-capable token before relying on it.
To uninstall, remove only the `dash0` plugin directory and reload plugins.

## Token accounting is on by default, and incomplete

Models and token counts are the point of the integration, so the helper reads
them unless you say otherwise. Nothing to configure: a fresh install accounts
for every turn.

To turn it off, set `export_usage: false` in either
`~/.amp/dash0-agent-plugin.local.md` or a workspace
`.amp/dash0-agent-plugin.local.md`:

```yaml
export_usage: false
```

Or in the environment of the Amp executor before it starts, which switches it
off for one session rather than permanently:

```sh
export AMP_PLUGIN_OPTION_EXPORT_USAGE=false
```

On PowerShell: `$env:AMP_PLUGIN_OPTION_EXPORT_USAGE = 'false'`.
`DASH0_EXPORT_USAGE=false` works too.

What this costs when left on: one `amp threads export` subprocess per turn,
polled for up to twenty seconds. It never becomes latency the user feels — the
helper is detached, see below — but it does require working Amp authentication
on the executor. Without it the read fails and the turn reports
`dash0.amp.usage.status=unavailable` instead of carrying tokens; the poll gives
up after two consecutive command failures rather than spending the whole window
on an executor that is logged out.

At `agent.end`, the helper runs the public read-only command
`amp threads export <thread-id>`. This requires working Amp authentication on
the executor. It reads the full export into bounded memory, selects only the
assistant message IDs supplied by that end event, and discards everything else.
The export JSON schema is **not a stable Amp API**. This is best-effort
compatibility with the shape observed in Amp
`0.0.1789407412-g29cb5a`, on September 14, 2026. A schema or permission change can
disable usage while lifecycle telemetry continues.

The trace contains:

```text
chat claude-sonnet-4-6            observed turn interval; the answering message's usage
  execute_tool shell_command      observed paired hook interval
  chat gpt-6-astra                an earlier exported assistant message's usage
```

The root is a `chat` span, as on every other harness: Dash0 reads a session's
turns from `chat` spans and treats `invoke_agent` as a sub-agent invocation, so
a model-neutral `invoke_agent` root left each exported model call standing alone
instead of under the turn that made it. The last selected record — the message
that answered the turn — names the root and supplies its usage. Earlier records
in the same turn stay separate zero-duration children rather than being summed
into one model's name, so a multi-model turn is still never misattributed.

If usage is disabled or unavailable the root is a bare `chat` span with no model
and no tokens: the turn is still recorded, only its accounting is missing. The
export does not hold the answering message when the turn ends, so the helper
polls for it — see the limit on export timing below for how long, and why that
wait costs the turn nothing.

Child usage spans are zero-duration observations at turn completion, tagged with
`dash0.amp.timing=observation`. They are not measurements of provider request
duration; this tag does not automatically exclude them from Dash0 metrics.
The Amp-reported `usage.model` is preserved verbatim; known model names use
shared provider inference.
Unknown providers remain absent rather than defaulting to OpenAI or Anthropic.

| Export field               | Span attribute                             | Meaning               |
| -------------------------- | ------------------------------------------ | --------------------- |
| `totalInputTokens`         | `gen_ai.usage.input_tokens`                | Cache-inclusive input |
| `outputTokens`             | `gen_ai.usage.output_tokens`               | Output                |
| `cacheReadInputTokens`     | `gen_ai.usage.cache_read.input_tokens`     | Subset of input       |
| `cacheCreationInputTokens` | `gen_ai.usage.cache_creation.input_tokens` | Subset of input       |

`inputTokens` is uncached input, used only for consistency checks. Cache counts
are not added to `totalInputTokens`. Missing/null counts stay absent, explicit
zero remains zero. No missing total is reconstructed. Invalid numbers or
contradictory counts invalidate that usage record. Duplicate aliases for one
record are counted once; ambiguous identities are omitted. No reasoning-token,
cache-TTL, cost, or billing values are inferred.

`dash0.amp.usage.status` on the root reports `disabled`, `unavailable`,
`unsupported`, `invalid`, `partial`, or `matched`. `matched` means the selected
records supplied usable usage, **not that the turn's bill is fully accounted
for**. Individual fields may still be absent. Hidden subagent, Oracle, finder,
compaction, retry, or tool-side inference not represented by those messages is
not counted. Thread-wide billing totals are never assigned to a turn or mixed
with this source. No final-message model is used to label other models' tokens.

### Turn totals and multimodal input

Sum the `chat` spans of a turn — its root and any usage children — to obtain its
recorded input and output tokens. Each selected export record contributes once,
even when the hook supplies both its numeric and string aliases. Separate records
using the same model remain separate calls. Tool spans carry no token totals, so
summing the trace does not count those tokens again. Dash0 calculates
model-based cost; the plugin does not calculate or export a price.

Multiple models and multiple content modalities are different. Amp supports
image content, but the usage shape consumed here does not expose separate image
or audio token counters. The plugin forwards the reported aggregate counters
without deriving extra tokens from images, bytes, URLs, or attachments. It does
not claim modality-specific pricing or complete audio accounting. Image URLs
and inline image payloads are discarded even with `omit_io: false`; only text
survives.
Image-heavy exports can exceed the 16 MiB limit and leave usage unavailable.

### Attribute and analytics compatibility

All spans use the shared identity, team, VCS, working-directory, and privacy
rules in [the development guide](../DEVELOPMENT.md#telemetry-attributes).
Unavailable values stay absent. Model/provider attributes come only from the
export, so the hooks alone establish no model for the root or for a tool. MCP
names, server attributes, and the URL, commit and line-count details use the
existing pipeline's shared tool enrichment. Raw error attributes are omitted;
content follows `omit_io`.

The turn root is a top-level `chat` span, matching the other harnesses, so the
Sessions API counts it as one turn of this thread and operation-based prompt and
request-latency metrics read it the same way they read a Claude or Codex turn.
No `invoke_agent` span is emitted: Amp's hooks expose no sub-agent ancestry, so
inventing one would misreport the turn as a sub-agent invocation. A multi-model
turn still emits one extra zero-duration `chat` child per earlier model call,
which those metrics count as an additional prompt with no measured latency.
The shared pipeline is unchanged.

## Lifecycle and operational limits

- Thread ID maps to `gen_ai.conversation.id`. Typed prompt IDs correlate starts
  and ends. `session.start` when opening an existing thread does not start a turn.
- `executor.kind` is reported as `local`, `remote`, or `unknown`. `remote` is not
  proof that an executor is an Orb. No cloud provider or Orb ID is guessed.
- No spans arrive until a matching end hook. Reload, shutdown, or interruption
  can lose in-flight or undelivered turns. There is no crash recovery, durable
  deduplication, or exactly-once guarantee. Do not synthesize session end on
  dispose. A new turn can reuse a prompt ID after the prior turn ended.
- Tool results need their matching call in the same active turn. Results after
  the end hook are omitted. No subagent ancestry is invented — Amp exposes no
  tool that spawns one. Durations are elapsed wall time, possibly including pauses.
- **Only client-side tools produce spans.** Amp executes some built-in tools on
  the server, and those fire no `tool.call` or `tool.result` at all, so a plugin
  is never told about them and no `execute_tool` span exists. A turn's tool
  spans are therefore not a complete record of the tools it used. Measured in a
  single turn: `shell_command` reached the plugin, `web_search` did not;
  `read_web_page` likewise produced no plugin event. The split is Amp's and can
  move, so treat those as examples rather than a list. There is nothing to fix
  in the bridge, and nothing should reconstruct the missing calls from
  `threads export` — that read has its own timing problem, below.
- At most 32 active threads, 512 tool calls and 512 selected assistant IDs per
  turn, and four concurrent deliveries. Overflow can drop data;
  `dash0.amp.truncated=true` marks per-turn truncation. Excess deliveries are
  dropped with a static diagnostic. Unfinished tools do not get fake end times.
- The helper limits envelopes to 1 MiB and exports to 16 MiB. Export has a
  five-second timeout. OTLP uses the shared retry policy, once per batch.
  Failures never reject tools or alter the assistant response. There is no
  cross-turn catch-up that could misattribute usage.
- Usage is not ready when the turn ends. `threads export` returns the thread
  with **no messages at all** for the first seconds after `agent.end`, then
  materializes it whole, complete with usage; measured locally at around seven
  seconds past CLI exit. So the helper waits before reading rather than after,
  and backs off by half each round — four exports cover the twenty-second
  window where a fixed one-second interval would have spent ten, and the first
  read lands past the empty stretch instead of inside it. It stops as soon as
  usage matches, and gives up early if the export _command_ fails twice, since
  a missing or logged-out `amp` will not improve by waiting.
  That poll must never become latency the user feels, because Amp awaits the
  `agent.end` handler. So the bridge waits only two seconds for the helper and
  then hands off: the helper is detached and outlives the CLI, which in
  `amp -x` mode exits as soon as the turn ends. **Spans for a turn can
  therefore arrive after the process that produced them is gone**, and a
  failure detected after the handoff reports itself through the plugin log
  rather than through the turn. A turn whose usage never lands still exports,
  carrying `dash0.amp.usage.status=partial`.

## Compatibility evidence

The integration reuses shared configuration, enrichment, span builders, and the
exporter. One completed-turn envelope lets those components run once for the
turn. Like Copilot, it combines lifecycle hooks with a separate usage source.
Amp keeps that source's per-model records rather than assigning a turn total to
one model.

Sources checked:

- [Plugin API](https://ampcode.com/docs/plugin-api): lifecycle, typed message IDs,
  executor identity; neither messages nor `agent.end` expose model/token usage.
- [External API](https://ampcode.com/api/external): per-model cumulative thread
  counts include subthreads and require workspace application credentials. These
  cannot establish exact turn ownership.
- [Streaming JSON](https://ampcode.com/docs/cli/streaming-json): optional assistant
  usage, but no reliable documented per-model accounting across all runtimes.
- [Threads](https://ampcode.com/docs/threads) and installed CLI help: public export
  command. `threads raw` is internal-only and is not used.
- [Orbs](https://ampcode.com/docs/orbs): executor-local installation and lifecycle.

Amp's private implementation was not available for inspection. In a live Linux
Orb CLI run, root and tool spans reached Dash0 with an ingest-only token and the
configured dataset. Native Orb exports joined all 10 frozen plugin assistant IDs
to their exact `protocolMessageID` fields. Repeated exports changed `v` from 60
to 211 as the thread progressed, so the integration does not treat it as a schema
version. Numeric legacy IDs have synthetic test coverage but were not checked on
an old CLI.

Automatic usage-enabled delivery was verified in a Linux Orb CLI run: all emitted
spans were queried back from Dash0, with a successful HTTP attempt and a completed
end hook. An earlier batch was observed only after manual replay; its original
delivery failure was not explained. Final assistant usage often appeared only
after `agent.end`. These checks do not establish complete billing or delivery on
every shutdown path, host, and OS. Automated local and remote callback tests are
not live host tests. The bridge no longer waits for delivery: it hands off after
two seconds and the detached helper finishes on its own, so completion latency
is bounded by that grace period rather than by the usage poll.

Run `make test-amp`, `go test -race ./...`, and `make lint`. The bridge tests use
the pinned public Amp types; Go tests exercise exact attribution and actual
subprocess failures without starting paid agent runs.
