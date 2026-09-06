# Live-session tests (L3)

```sh
make test-live            # or: ./test/live/opencode/run.sh
```

Drives the real `opencode` binary through one scripted session and asserts the
spans that reach a mock collector.

Every other test layer replays events we recorded earlier, so none of them can
notice the plugin failing to load, a hook that stopped firing, or an OpenCode
upgrade renaming a bus field. This layer is the only one that runs the agent.

OpenCode is the first runtime here that can be driven headlessly — `opencode run
"<prompt>"` completes a turn and exits — which is why the layer exists for it and
not for the other four.

## How it stays deterministic

The model is `test/capture/opencode/mock-llm.mjs`, the same scripted
OpenAI-compatible server the capture harness uses, wired in through
`provider.mock.options.baseURL`. It scripts one turn: assistant text, a
successful `read`, a failing `read`, an MCP tool call, a delegation, and fixed
usage numbers on every response. One scripted turn lives in one place — a second
copy here would drift from the fixture the golden tests replay.

Each session runs under a throwaway `HOME` and `XDG_*` tree with the plugin
installed from this checkout and the binary seeded from a local build, so a run
touches neither your `~/.config/opencode` nor a published release.

## What it asserts

- The six spans and their names, with anything outside `chat`, `execute_tool`
  and `invoke_agent` treated as a regression.
- One trace id, and no span pointing at a parent that was never exported.
- `chat` at the root; tool spans beneath it; `invoke_agent` beneath the `Agent`
  tool span, carrying usage of its own.
- The MCP call arriving split into its tool and its server, not as OpenCode's
  flat `capture_echo`.
- The failing call reporting an error status.
- Under the default `omit_io`: prompts keep their envelope with `<REDACTED>`
  content and a withheld-character count, while tool arguments and results are
  omitted outright.
- The same probes run again at `prompts: full, tools: disabled` and must report
  the inverse. Without that, a probe that quietly stopped matching would still
  read green.
- `opencode run` exits 0 and reports nothing to the user when the collector is
  unreachable, the endpoint rejects, the config file is malformed, or the cached
  binary is corrupt.

`cache_creation.input_tokens` is asserted present but not positive: the OpenAI
wire format the mock speaks has no cache-write field to carry one.

## When it fails

The run prints the span tree and writes the full span JSON to the sandbox; the
path is in the output. The sandbox survives the run, so `opencode.log` beside it
holds what OpenCode itself printed.
