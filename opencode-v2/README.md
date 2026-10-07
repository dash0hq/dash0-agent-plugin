# Dash0 plugin for OpenCode V2

This adapter uses the OpenCode **V2** `Plugin.define` API.

## Install

```bash
opencode plugin add @dash0/agent-plugin-opencode-v2
opencode reload
```

OpenCode installs the package from npm and adds it to the global
`opencode.json`. Then, in an OpenCode session, run `/dash0-configure`. The
skill ships with the plugin and writes the OTLP URL, token and options to
`~/.opencode-v2/dash0-agent-plugin.local.md`, so the token never has to sit in
`opencode.json`. Run `opencode reload` again afterwards; the exporter reads its
settings when it starts. (`opencode reload` targets the background service; add
`--server <url>` for your own `opencode serve`.)

Until a release includes the OpenCode binary assets and the npm package, use
the clone install and the `executable` option described below. The bootstrap
cannot download an asset that has not been released.

## Install from a clone

Node.js is required. Clone this repository, install the pinned JavaScript
dependency, and add the absolute package path to `opencode.json`:

```bash
git clone https://github.com/dash0hq/dash0-agent-plugin.git
cd dash0-agent-plugin/opencode-v2
npm ci
pwd
```

```json
{
  "plugins": [
    {
      "package": "/absolute/path/to/dash0-agent-plugin/opencode-v2",
      "options": {
        "otlp_url": "https://ingress.example.com:4318",
        "auth_token": "your-token",
        "dataset": "default",
        "agent_name": "opencode",
        "team_name": "my-team",
        "omit_io": true,
        "omit_user_info": false,
        "omit_identity_fallback": false,
        "debug": false,
        "debug_file": "/tmp/dash0-opencode.log"
      }
    }
  ]
}
```

Use a project `opencode.json` for that checkout or OpenCode's global config for
all projects. Every option is optional here: `/dash0-configure` writes them to
the config file below instead. Do not commit an authentication token. Run
`opencode reload` after changing the plugin entry.

The package option names above are also accepted in
`.opencode-v2/dash0-agent-plugin.local.md` frontmatter. Project configuration takes
precedence over the user file at `~/.opencode-v2/dash0-agent-plugin.local.md`; files
do not merge. Package options override the file; non-secret options then fall
back to `DASH0_<OPTION>`. The token uses `OPENCODE_V2_PLUGIN_OPTION_AUTH_TOKEN` or
the file's `auth_token`, never `DASH0_AUTH_TOKEN`. Package options are passed to
the consumer as `OPENCODE_V2_PLUGIN_OPTION_<OPTION>` without changing the server's
environment.

```markdown
---
otlp_url: https://ingress.example.com:4318
dataset: default
agent_name: opencode
team_name: my-team
omit_io: true
omit_user_info: false
omit_identity_fallback: false
debug: false
---
```

On macOS, `auth_token_keychain_service` (and optionally
`auth_token_keychain_account`) reads the token from a keychain generic password
instead; a keychain token takes precedence over every other source.

`enabled: false` disables telemetry, and is the one key that does not follow the
precedence above: `false` in either the package options or the config file
turns it off, so a project file can opt a checkout out. `otlp_url` is required
to export telemetry.
`dataset`, `agent_name`, `team_name`, `debug`, and `debug_file` have the same
meaning as in the other adapters. Privacy defaults are `omit_io: true`,
`omit_user_info: false`, and `omit_identity_fallback: false`: prompts and tool
I/O are redacted by default, while user attribution is enabled and may fall back
to the OS account unless explicitly disabled.

The adapter emits one `chat` span per execution, `execute_tool` spans, and
`invoke_agent` spans for subagents. Step token counts are summed within the
execution; cache tokens are included in input totals and reasoning tokens in
output totals. Child usage is not added to parent usage. Skill names are
attributed when V2 exposes them. MCP tools keep V2's flattened
`<server>_<tool>` name and carry no `dash0.gen_ai.tool.mcp_server`: the plugin
API cannot tell an MCP tool from a local tool in the same namespace. Provider headers, provider
state, reasoning text, and raw error-response bodies are never exported.
With `omit_io: true`, free-form errors are also replaced by their error type.
With `omit_user_info: true`, names are hashed and email addresses omitted.

## Use an unreleased local Go build

The normal shell or PowerShell bootstrap downloads the versioned, checksummed
`opencode-v2-on-event` release binary. To test code that has not been released,
build it and set the V2-only `executable` package option to its absolute path:

```bash
cd /absolute/path/to/dash0-agent-plugin
go build -o /tmp/opencode-v2-on-event ./cmd/opencode-v2-on-event
```

```json
{
  "plugins": [{
    "package": "/absolute/path/to/dash0-agent-plugin/opencode-v2",
    "options": { "executable": "/tmp/opencode-v2-on-event" }
  }]
}
```

On Windows, build an `.exe` and use its absolute path. The executable receives
newline-delimited V2 events on standard input.

## Uninstall

Run `opencode plugin remove @dash0/agent-plugin-opencode-v2` (the same specifier you
added), or remove the package entry from `plugins` in `opencode.json`, then
run `opencode reload`. Remove the clone if you used
one, and `~/.opencode-v2/` if you no longer need its settings. Cached binaries
and session state are under `$OPENCODE_V2_PLUGIN_DATA`, `$DASH0_PLUGIN_DATA`, or by
default `~/.local/state/dash0-agent-plugin/opencode-v2`.

## Current limitation

The V2 API provides a live, stream-only event subscription. During a plugin
reload, events between stopping the old subscription and starting the new one
may be missed. There is no reliable replay API available in plugin context, so
the adapter cannot backfill that gap.

Correlation survives consumer reloads within one server process. A server
restart creates a separate state namespace; namespaces and sessions idle for
24 hours are removed. Failed exports use the shared
exporter's bounded retries; there is no durable delivery queue.
