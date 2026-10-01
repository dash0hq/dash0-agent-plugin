# OpenCode event capture

Scaffolding to record a real OpenCode session's full bus-event and hook stream,
so the `internal/source/opencode` normalizer and the TypeScript plugin are
written against observed payloads rather than inferred ones.

Unlike the Cursor and Copilot captures — which are shell scripts wired in as
hook commands — OpenCode has no hook-command surface. The capture is instead a
throwaway **plugin** (`plugin/capture.ts`) that subscribes to the event bus and
to every `Hooks` callback, appending one JSON object per line.

## Run

```bash
test/capture/opencode/capture.sh [output.jsonl]
```

Everything is self-contained: the script builds a throwaway `HOME`, an empty
project, and points OpenCode at

- `mock-llm.mjs` — a scripted OpenAI-compatible server, wired in via
  `provider.mock.options.baseURL` with a dummy `apiKey`. It drives one
  deterministic turn: a successful `read`, a failing `read`, an MCP tool call,
  a delegated sub-agent, and a closing text step.
- `mock-mcp.mjs` — a one-tool stdio MCP server, so the capture contains a real
  MCP-provided tool call.

No model credentials and no network are needed, and the same turn is produced
every run.

Outputs, all under `captured/` (git-ignored) and all truncated per run:

| File | Contents |
|---|---|
| `captured_events.jsonl` | the capture itself — one record per bus event and hook call |
| `llm-requests.jsonl` | every request OpenCode sent the mock provider, with headers; the script's own readiness poll is tagged `?probe=1` and excluded |
| `opencode.log` | OpenCode's own `--print-logs` output |
| `storage.txt` | the sandbox `opencode.db` schema, per-table row counts and a sample `session` / assistant-`message` row, plus the data-dir file tree |

`OPENCODE_CAPTURE_CACHE` overrides the models.dev cache directory (defaults to
`$HOME/.cache`); a cold cache makes OpenCode's first start hang well past any
sane timeout, so the real one is reused by default.

## Record shape

```jsonc
{"seq": 0, "ts": 1787420800000, "kind": "plugin", "name": "init",  "payload": {…}}
{"seq": 1, "ts": 1787420800001, "kind": "event",  "name": "session.created", "payload": {…}}
{"seq": 3, "ts": 1787420800003, "kind": "hook",   "name": "tool.execute.before", "payload": {"input": …, "output": …}}
```

`seq` is a monotonic counter, so the fixture preserves firing order across
events and hooks — which is what anchors the trace-context lifecycle.

## Promoting a capture

The reference fixture lives at
`internal/source/opencode/testdata/captured_events.jsonl`:

```bash
cp test/capture/opencode/captured/captured_events.jsonl \
   internal/source/opencode/testdata/captured_events.jsonl
```

The findings drawn from it — sub-agent lifecycle, MCP tool naming, terminal
tool-part semantics, on-disk session storage — are written up in
`opencode/README.md`.
