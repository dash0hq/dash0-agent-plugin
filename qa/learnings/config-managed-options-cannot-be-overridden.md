# A managed plugin install's options cannot be overridden for one session

On a Dash0 machine `dash0-agent-plugin` is installed with `scope: managed`, and its
configuration lives in `~/.claude/remote-settings.json` under
`pluginConfigs/dash0-agent-plugin@dash0/options`. Claude Code injects those as
`CLAUDE_PLUGIN_OPTION_*`, and `harness.PluginOption` prefers that form over every
`DASH0_*` value (`internal/harness/harness.go`).

Three ways to override it were tried and all failed:

| Attempt | Result |
| --- | --- |
| `env -u CLAUDE_PLUGIN_OPTION_OTLP_URL` before `claude` | Claude Code re-injects it when it spawns the hook |
| `--settings` with its own `pluginConfigs` | Remote settings win |
| A project-level config file setting `otlp_url` | Exports `DASH0_OTLP_URL`, which loses |

The tell that an override failed is `service.name` on the emitted span. The managed
options set `AGENT_NAME=claude`, so a span reading `claude` rather than the default
`claude-code` means the managed layer is still in charge.

**Why it matters:** a QA run cannot choose the endpoint, the dataset, `omit_io`, or the
debug payload log. Every design that assumes it can produces a session that exports
somewhere else while the intended destination records nothing, which reads as the plugin
sending no telemetry at all.

**How to apply:** treat the installed configuration as fixed and observe it, rather than
reconfiguring it. Add QA behaviour as a second hook handler registered in the session's
own `.claude/settings.json`, which is handed no plugin options. See
[[config-project-file-leaks-into-every-registration]] for the trap on the other side.
