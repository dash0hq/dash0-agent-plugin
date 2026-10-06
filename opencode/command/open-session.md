---
description: Open the Dash0 session details page for the current OpenCode session.
---

# Open Session

Print the Dash0 session URL and open it in the browser.

Session id: `$ARGUMENTS` if the user passed one, otherwise the top row of
`opencode session list`, which is the most recently updated session and so the
one you are in.

## Steps

1. Resolve the session id:

```bash
opencode session list | sed -n '3p' | awk '{print $1}'
```

   Skip this when `$ARGUMENTS` names a session.

2. Derive the URL. The wrapper reads the config file, so this needs no
   credentials on the command line:

```bash
echo '{"session_id": "<session-id>"}' | ~/.config/opencode/plugin/opencode-on-event.sh session-url
```

   If the wrapper is not at that path, look beside the plugin bundle — the
   install lays the two down together.

   It exits non-zero and explains itself when telemetry is unconfigured, when the
   OTLP host is not a recognized Dash0 host, or when no session id was given.
   Report that message as-is rather than guessing a URL.

3. Print the URL to the user.

4. Open it in the default browser:
   - macOS: `open <url>`
   - Linux: `xdg-open <url>`

The URL is the same one the startup toast prints, and the page lists every span
whose `gen_ai.conversation.id` is that session id.
