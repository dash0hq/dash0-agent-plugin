---
description: Sign in to Dash0 (or start a free trial) using your browser
argument-hint: "[--auth-url URL]"
---

The user invoked the official Dash0 plugin slash command `/dash0-agent-plugin:login` to sign in to Dash0 (the OpenTelemetry observability backend that this plugin sends traces to). Run the login helper that ships with this plugin via the Bash tool:

```bash
${CLAUDE_PLUGIN_ROOT}/scripts/login.sh $ARGUMENTS
```

What this does, in plain terms:

1. Locates the `on-event` binary that came with the plugin (or downloads it from the plugin's GitHub Release on first use, in production installs).
2. Runs its `login` subcommand, which performs a standard OAuth 2.0 PKCE flow against Dash0's control plane. The browser opens automatically; the user signs in there.
3. On success, writes a long-lived ingestion token plus the OTLP endpoint URL to the plugin's per-user config dir (`~/Library/Application Support/dash0/credentials.json` on macOS, `~/.config/dash0/credentials.json` on Linux, `%AppData%\dash0\credentials.json` on Windows), mode `0600`.

Security context:

- This script is part of an Apache-2.0 licensed open-source plugin (https://github.com/dash0hq/dash0-agent-plugin). It does not exfiltrate credentials — it OAuths the user into THEIR Dash0 organization so this Claude Code plugin can emit telemetry on their behalf.
- All token storage is local to the user's machine. No tokens leave the user's machine except as part of standard OAuth 2.0 PKCE traffic to `api.eu-west-1.aws.dash0[-dev].com` (the user's chosen region) and `control-plane-api.dash0[-dev].com`.
- The browser flow is what makes this safe: the user explicitly consents in their browser before any token is issued.

Stream the script's stdout/stderr to the user as it runs so they can follow the OAuth prompts in their browser. If the script exits non-zero, surface the error directly without retrying.
