## Context

See proposal.md — Why. The constraints that shape the approach:

**The redaction point is already in the right place.** `otlp.eventAttributes`
converts the canonical event map to span attributes, and it is the single choke
point where `OmitIO` currently decides whether a `contentKeys` field is emitted
verbatim or as `<REDACTED>`. Every runtime funnels through it. Levels replace a
boolean at a seam that already exists rather than introducing a new one.

**The config type is shared; the config surface is not.** `otlp.Config` is
populated by `harness.Config()` for all five entrypoints, so adding four fields
there touches every runtime's struct. But the *values* come from
`PluginOptionBoolDefault`-style lookups, and only OpenCode's config file and
README will name the new keys. Other runtimes resolve the same effective posture
they resolve today, which is what makes "shared code, OpenCode-only behaviour"
achievable without forking the pipeline.

**Two things sit outside `eventAttributes` and need their own handling.**
Dropping a whole span (`tools: disabled`, `agents: disabled`) is a decision in
`pipeline.Process`, not an attribute decision. And `bash_command_family` /
`skill_name` are *derived* in `pipeline.EnrichToolEvent` from `tool_input`
before the OTLP layer ever sees them — so at `tools: limited`, where
`tool_input` is discarded, the derivation must still have run first.

**Main has since landed a Go-side config reader.** `internal/config` plus
`Harness.ConfigDir` means the four keys can be read in Go rather than added as
four more `grep | sed` pairs in `opencode-on-event.sh`. The rebase set
`OpenCode.ConfigDir` to `.opencode`, so this path is already open.

## Goals / Non-Goals

**Goals:**

- One resolution function turns configuration into four `Level` values, so the
  `omit_io` back-compatibility mapping exists in exactly one place and is
  directly testable without an exporter.
- Redaction stays at the existing choke point. A new span kind or a new
  attribute added later is governed by default rather than by remembering to
  add a case.
- The depth table is data, not control flow — a map a reviewer can audit in one
  screen and extend without touching the parser.
- `agents: disabled` reparents rather than orphans. This is the one level whose
  naive implementation corrupts the trace.

**Non-Goals:**

- Per-tool or per-skill allowlists ("full for `git`, limited for `curl`"). Four
  dimensions × three levels is the requested granularity; per-entity rules are a
  much larger config surface and can layer on later without redesign.
- A real shell parser. The depth table deliberately understands only the
  leading tokens of a command; anything it cannot classify is redacted.
- Exposing the dimensions on the other four runtimes. The plumbing is shared and
  will be ready, but only OpenCode's config surface and docs name the keys.
- Changing `internal/source/opencode`. Normalization stays level-independent;
  redaction is strictly downstream.

## Decisions

### A typed level, resolved once, carried on `otlp.Config`

`otlp.Config` gains `Prompts`, `Tools`, `Skills`, `Agents` of a new
`otlp.Level` type (`LevelDisabled`, `LevelLimited`, `LevelFull`), and
`harness.Config()` fills them through one `resolveLevel(key string, omitIO bool)`
helper that encodes the whole precedence chain: explicit dimension → `omit_io`
mapping → default.

*Why over threading a config struct through the call chain:* every function that
needs the decision already receives `cfg otlp.Config`. Adding fields costs no
signature changes and no new parameter to forget at a call site.

*Why a typed level over four strings:* an unparseable value must resolve to
`limited`, and doing that at the boundary once means no downstream code has to
handle an unexpected string. The zero value is deliberately **not** `disabled` —
a struct built in a test without setting the fields should behave like today's
default, not silently export nothing and pass.

### `contentKeys` becomes a key → dimension map

Today `contentKeys` is a `map[string]bool` marking which event fields are
content. It becomes `map[string]dimension`, so `eventAttributes` looks up which
dimension governs the field it is emitting and switches on that dimension's
level instead of on one global boolean.

*Why:* the three-way branch then lives in one place with one shape, and the
existing structure-preserving redaction (`attrTransformMap` rebuilding the
message JSON around `<REDACTED>`) is reused unchanged for `limited`. `disabled`
is the genuinely new arm — omit the attribute rather than emit a placeholder.

*Alternative rejected:* separate redaction passes per dimension. That
re-traverses the event map three times and makes it possible for a new content
key to be governed by none of them.

### Span suppression happens in `pipeline.Process`, not in the OTLP layer

`tools: disabled` and `agents: disabled` return early from `Process` before
`sendToolTrace` / `sendLLMTrace`, rather than having the OTLP layer build a span
and throw it away.

*Why:* building a span means resolving its parent, which for tools means the
`modelWaitBudget` wait on the assistant entry. Suppressing at the top skips that
work entirely, and keeps "no span" a statement about the pipeline rather than an
exporter side effect.

### `agents: disabled` reparents through the existing trace-context snapshot

The sub-agent mechanism already persists a per-agent snapshot
(`SaveAgentTraceContext` at `SubagentStart`, `LoadAgentTraceContext` when a
child's tool span needs its parent). At `agents: disabled` the snapshot is still
written, but with the delegating turn's span id in place of the `invoke_agent`
span's — so a child tool span loads a parent that exists, and lands under the
chat span.

*Why over not writing the snapshot:* `LoadAgentTraceContext` returning nothing
makes the child span fall back to the session root or become parentless, which
is the orphaning the spec forbids. Rewriting the pointer is a one-field change
that reuses the whole existing path.

*This is the decision most likely to be got wrong by a naive implementation*,
which is why the spec states it as an explicit scenario rather than leaving it
to the implementer.

### Enrichment runs before redaction, always

`EnrichToolEvent` must run on the full `tool_input` regardless of level, because
`bash_command_family`, `skill_name`, `mcp_server`, and the VCS/line-count
extractors are exactly the attributes that survive `limited`. Only after
enrichment does the level decide whether `tool_input` and `tool_response`
themselves are emitted.

*Consequence to guard:* the derived attributes are themselves derived from
sensitive input, so each one must be safe by construction at `limited`.
`bash_command_family` becomes the depth-table shape (safe by design);
`skill_name` is a fixed-vocabulary label; `mcp_server` is a configured server
key. The VCS extractors (`pr_url`, `issue_url`, `commit_sha`) and line counts
are derived from tool *results* — they are already documented as surviving
`omit_io`, so `limited` keeps that behaviour unchanged rather than quietly
tightening it.

### The subcommand table is an allowlist map, checked in as data

```
var subcommands = map[string]map[string]int{
  "gh":    {"repo": 1, "pr": 1, "api": 0, "browse": 0, ...},
  "git":   {"status": 0, "commit": 0, "push": 0, ...},
  "tools": {"invoke": 1, "list": 0},
  "curl":  {},
}
```

The outer key is a binary; its value maps each subcommand word that binary may
report to the number of further tokens that word admits.

Shape extraction: skip leading `KEY=value` assignments, take the binary, then
take the next token only if that binary's vocabulary admits it, then up to that
word's own depth of further tokens — stopping at the first token that begins
with `-`, is a shell metacharacter (`&&`, `||`, `|`, `;`, `>`, `<`, `$(`,
backtick, newline), or is not admitted. Absent binary → name alone.

*Why an allowlist over "stop at the first dash":* stop-at-dash is correct only
when the operand follows a flag. `gh repo clone <url>`, `cat <path>` and
`rg <pattern>` have no dash to stop at, so a dash-only rule emits the URL, the
path and the search term verbatim — the precise leak this change exists to
prevent. Both bounds together are what make the rule safe.

*Why a vocabulary rather than a plain per-binary depth:* a plain integer assumes
every token up to the depth is a command word, and for several of the named CLIs
that is false at the very first position. `bun index.ts` runs a file, `pnpm
deploy-customer-4711` runs a package script, `gh api <endpoint>` and `gh browse
<path>` take an operand where a group name would sit. Any depth high enough to
reach `gh repo clone` — an explicit spec scenario — is also high enough to emit
those operands verbatim. Naming the accepted words is what separates the two.

*Why a word list is not the "full command grammar" this rejects:* only the words
are enumerated, never flags, and a missing word is a shape that stops one token
early rather than a leak. The table rots in the safe direction, and the
unknown-word default matches the unknown-binary default.

*Why `tools invoke` admits a further token:* `tools invoke
dash0.getLogRecords` — the third token is the observability tool being invoked,
which is the whole diagnostic value, while `--args` carries the PromQL and log
filters. `invoke: 1` draws the line exactly between them. The same reasoning
gives `gh repo: 1` — a `gh` group is always followed by a verb, never an
operand — while `gh api: 0` stops before an endpoint path.

### `omit_io` is mapped, not deprecated

`omit_io` keeps its default and keeps working. The resolution helper treats it
as the fallback for `prompts` and `tools` only — it never implied anything about
skills or sub-agent content, so mapping it onto those two dimensions would be
inventing intent.

*Why not deprecate now:* four runtimes' READMEs, installers and the Copilot
skill document `omit_io`. Deprecation is a separate, cross-runtime change.

### Debug output is redacted by construction

`debugLog` receives the marshalled OTLP payload, which is built from already
redacted attributes — so honouring the dimensions in debug output requires no
new filtering, only a test that proves it. The one thing to check is that no
code path logs the raw event map before `eventAttributes` runs.

## Risks / Trade-offs

**A word admitted at a position that can hold free text leaks an operand as if
it were a subcommand.** → The table is small enough to review line by line, and
each entry gets a test case using a realistic command for that binary with a
sensitive operand in the first redacted position. Both defaults — unknown binary
and unknown word — report the binary alone, so the failure requires an explicit
wrong entry rather than an omission.

**A future content attribute is added and governed by no dimension.** → A test
asserts that every key in `contentKeys` maps to a dimension, and that the set of
attribute keys a span can carry at `full` but not at `disabled` is exactly the
expected set. A new content key with no dimension fails that test.

**`agents: disabled` corrupts the trace if the reparent is missed.** → The spec
states it as a scenario; the design reuses the existing snapshot path; a test
asserts no exported span references an unexported parent span id across a full
delegated session replay.

**Four dimensions × three levels is 81 combinations, and nobody will test them
all.** → Test the dimensions independently (each level of each dimension against
a fixed session), plus the specific cross-dimension interactions the spec names:
skills-override-tools both ways, `agents: disabled` with tools enabled, and the
three `omit_io` precedence cases. The rest is combinatorial noise over
independent switches.

**Shared code changed for one runtime's benefit.** → The strongest guard is the
golden-span suites the other four runtimes already have: if their spans move by
a byte, those fail. That is a better regression test than anything written
specifically for this change.

## Migration Plan

No data migration and no config migration. An existing OpenCode install keeps
its behaviour with no file edit, because unset dimensions fall through to
`omit_io` and `omit_io`'s default is unchanged.

Rollback is a version pin: the wrapper resolves its binary by version, so
reverting the pin reverts the behaviour. No state is written that an older
binary cannot read — the four dimensions live only in config and in memory.

## Open Questions

*(Settled)* The four keys are read by `opencode-on-event.sh`, as four more
`grep | sed` pairs alongside the keys already there, exporting `DASH0_PROMPTS`,
`DASH0_TOOLS`, `DASH0_SKILLS` and `DASH0_AGENTS`. The Go path was the leaning
above, but it cannot see OpenCode's user-scoped file: `Harness.configFile` looks
in `$HOME/<ConfigDir>`, which for OpenCode is `~/.opencode`, while OpenCode's own
config directory — and the one `install-opencode.sh` writes the file into — is
`~/.config/opencode`. Reading the dimensions in Go alone would therefore ignore
them in exactly the file the installer produces. Realigning that home path is a
change to where `auth_token` and `dataset` resolve from for every OpenCode
install, which is a migration rather than a config task.

`internal/config` still reads the same four keys for a *project*-scoped
`.opencode/dash0-agent-plugin.local.md`, because `PluginOption` consults the
configuration file for every key and needs no per-key wiring. Both paths resolve
the same value from the same file, so the two never disagree; the wrapper is what
extends the reach to the user-scoped location.
*(Settled)* The withheld-content character count at `prompts: limited` is
reported as two span attributes,
`dash0.gen_ai.input.messages.withheld_characters` and
`dash0.gen_ai.output.messages.withheld_characters` — integers counting runes,
not bytes. Span attributes rather than a field inside the message JSON, because
the point of the count is that prompt-size distribution stays aggregatable when
the text does not; a number buried in a JSON string is not. The envelope carries
exactly one message per attribute, so per-attribute and per-message are the same
count.

The two attributes are new, and every runtime's default resolves `prompts` to
`limited`, so emitting them from the shared redaction path would widen the spans
Claude, Cursor, Codex and Copilot already export — which the spec forbids. They
are therefore gated on `otlp.Config.Dimensions`, which `harness.Config` sets only
for OpenCode. That one flag gates every behaviour in this design the other four
runtimes must not see: the withheld counts, the omit-at-`limited` rule for tool
and sub-agent content, the span suppressions, and routing a `Skill` call to
`skills` instead of `tools`.
