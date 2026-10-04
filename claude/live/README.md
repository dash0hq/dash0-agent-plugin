# dash0-live: Dash0 live insights inside Claude Code

> **Prototype. Local only, not shipped.** Built on Claude Code *function hooks* ("mods"), an
> early-access feature that needs `CLAUDE_CODE_ENABLE_FUNCTION_HOOKS=1`. It lives beside the shipped
> plugin and is not referenced by `.claude-plugin/plugin.json`.

The goal is to show the Dash0 live insights that `agent0-responder` shows on mobile inside Claude Code,
so people don't have to switch to the web UI.

## What it draws

| Surface | What | Interaction |
|---|---|---|
| Status line | `dash0: 1 critical 2 degraded`, `dash0: all clear`, or `dash0: offline` | none |
| Bar above the prompt (`AbovePrompt`) | `Dash0 ● 1 critical ● 2 degraded  3 resolved · <top failing summary> · 1h · 20s ago` | `Open`, `↻`, `Hide` (mouse) |
| Pane (`/dash0`) | Tabs **Overview / Checks / Agent0**: severity KPIs, failing checks with age, services, owner and priority, recent Agent0 threads | `1` `2` `3` switch tabs, `r` refreshes, **Investigate** puts a draft prompt in the composer, **Copy Dash0 link** |

`/dash0 [checks|agent0|refresh|show|hide]` mirrors every key, because pane hotkeys only work while the
pane has focus.

Data that has not refreshed for three polling periods is labelled **stale**. A failed source shows
**offline**, never the last counts under a live label.

## Load it

```bash
export CLAUDE_CODE_ENABLE_FUNCTION_HOOKS=1
claude --plugin-dir ./claude/live      # also needs the Dash0 MCP server connected (see /mcp)
```

Allow the two tools it polls, or Claude Code asks before every refresh (seen on 2.1.289 in manual
mode, although the engine docs say a plugin's `$.mcp.call` needs no grant):

```json
{ "permissions": { "allow": ["mcp__Dash0__getFailedChecks", "mcp__Dash0__listAgent0Threads"] } }
```

Use the server's name as `/mcp` lists it (`mcp__claude_ai_Dash0__…` for a claude.ai connector).

Options go in `~/.claude/settings.json` → `pluginConfigs["dash0-live"].options`: `dataset` (`default`),
`window` (`1h`), `refreshSeconds` (`60`, clamped 15–3600), `band` (`always` | `failing` | `off`) and
`mcpServer` (blank tries `Dash0`, `dash0`, then `claude.ai Dash0`).

## Check it

```bash
claude plugin validate ./claude/live
CLAUDE_CODE_ENABLE_FUNCTION_HOOKS=1 claude plugin test ./claude/live          # engine tests, MCP mocked
node --experimental-strip-types --test claude/live/test/*.spec.ts             # pure model + views
```

What is covered:

| Layer | Count | What |
|---|---|---|
| Node specs | 19 | Parser against fixtures and 40 live rows, empty answer, short rows, tool-error text, sorting, counts, staleness, band and pane trees |
| Engine tests | 11 | Server discovery and pinning, 403 tool error, no server, empty answer, Agent0 failure isolation, start-up warm-up, polling and session end, `/dash0` tabs/hide/show, band hide button, copy link, Investigate draft; band and pane on terminal, desktop, vscode and mobile, docked and inline |
| Typecheck | – | `tsc --strict --noUncheckedIndexedAccess` over hooks and engine tests |
| Mutation checks | 8 | Each guarded behaviour broken by hand; a test failed every time |
| Real CLI (tmux) | – | Claude Code 2.1.289 with a stdio mock named `Dash0`: bar, status line, pane tabs by hotkey, Tab-focus + Enter on Investigate, `/dash0 hide`/`show`, 403 message, narrow docked pane |

The real-CLI run found three things the mocked engine could not: the permission prompt above, the MCP
server connecting after `session.start` (now a 2/4/8/16 s warm-up), and the bar wrapping its counts in
a real terminal (only the summary shrinks now).

## Design

### Layout (hexagonal, like agent0-responder)

| File | Holds | Touches `$` |
|---|---|---|
| `types/index.d.ts` | Domain types + the `$.state` contract | – |
| `hooks/model.ts` | Markdown table parser, severity ordering, counts, staleness, status line | no |
| `hooks/source.ts` | `InsightSource` port + the MCP adapter (checks first-class, threads best-effort) | no (gets an injected `call`) |
| `hooks/views.ts` | `bandView` / `paneView` built only from `Box`, `Text`, `Button`, which every surface has | no |
| `hooks/register.ts` | Hooks, polling, `$.mcp.call`, prompt fill, clipboard | **yes** |

This mirrors `agent0-responder`'s split: `src/lib/insights` is pure, `src/services/insights` holds the
adapters, and `use-insights` does the wiring. Severity colours come from the same Dash0 tokens the app
uses: `red-600`, `amber-500` and `emerald-500`.

### Authentication: what can be reused

The shipped plugin's `AUTH_TOKEN` (keychain → `pluginConfigs` → `~/.claude/dash0-agent-plugin.local.md`)
is an **OTLP ingest token**. The README tells users to scope it to ingest-only, so in most installs it
**cannot read** `/api/alerting/failed-checks`. Its *resolution chain* can be reused, but the token in
it usually can't. That gives two read paths:

1. **Now: Claude Code's own Dash0 MCP connection.** `$.mcp.call(server, tool, args)` runs on the
   engine's existing MCP connection and its OAuth session. The mod never sees a secret, and there is no
   new login. The cost is that MCP tools answer LLM-oriented markdown, so `model.ts` parses tables.
   That contract is pinned by fixtures captured from the live server, and the parser fails soft, showing
   `offline: unrecognised …` and never throwing.
2. **Next: the plugin's Go binary as a JSON source.** Add a subcommand next to the existing
   `session-url` (`claude-on-event.sh insights --json`) that resolves credentials through `harness`
   (same keychain/config precedence, plus a new `API_TOKEN` read-scoped option). It would call the same
   REST endpoints as the responder's `insight-source.ts`: `/api/alerting/failed-checks`, `/api/spans`
   (error rate), `/api/audit-logs` (deployments) and `/api/metrics`. The mod runs it through
   `$.process.run` with fixed argv, so the token never enters the sandbox. A later
   `claude-on-event.sh login` could port the responder's OAuth (RFC 8414 discovery → RFC 7591 DCR →
   PKCE) with an RFC 8252 loopback redirect, which replaces the static token entirely.

Both implement the same `InsightSource` port; `register.ts` would pick JSON when the binary answers and
fall back to MCP.

### Trust boundaries

- Check summaries and labels are telemetry, so they are untrusted. They are **drawn**, never handed to
  the model. The `/dash0` command output, which the model reads, carries counts only.
- **Investigate** uses `$.prompt.fill`, not `$.prompt.submit`. It names the check by id and rule, and
  the person reviews and sends the draft.
- `dataset`, `window` and `refreshSeconds` are validated and clamped before use.

## Plan

| Phase | Scope | Exit criterion |
|---|---|---|
| **0 (this)** | MCP-backed status line, bar and pane; failing checks + Agent0 threads; tests | Validate, strict `tsc`, 19 Node specs, 11 engine tests, real-CLI run against a mock ✅ |
| 1 | Run it against the real Dash0 MCP server in a person's terminal and in the desktop app; confirm the permission behaviour with the engine team | Desktop screenshot; no prompt, or allow rules documented as required |
| 2 | Go `insights --json` subcommand + `API_TOKEN` option; add error-rate, deployment and resource-pressure insights by porting the responder's normalizers to Go | Structured source preferred, MCP fallback; parity with the responder's four signals |
| 3 | Alert detail view (responder spec 019): description, annotations, affected resources, via `getFailedCheckDetails`; *new since last look* using `$.store` seen-markers (responder's `seen-marker-store`) and a toast | Detail opens from a check row; new-failure toast once per check |
| 4 | Agent0 from the pane: open a thread or `runTask` on a check, with streamed status | Thread opens; investigation status shows in the pane |
| 5 | Ship: fold the module into the main plugin once function hooks leave early access; declare the Dash0 MCP server in the manifest so `$.mcp.connect` works without manual setup | One install gives telemetry export + live bar |

### Open questions (verify on a real build)

- Can one plugin carry both `hooks.json` command hooks and a `modules` hooks module? Until confirmed,
  the mod stays a separate plugin folder.
- What name will plugin users' Dash0 MCP server have? Server discovery covers three spellings, and
  phase 5 removes the guess.
- Does the Dash0 OAuth server accept RFC 8252 loopback redirect URIs for dynamically registered native
  clients? This gates the phase-2 `login`.
- What is the right polling cost? 60 s × 2 MCP calls per open session. The bar could slow down while
  the session is idle.
