---
description: Open the Dash0 session details page for the current session.
---

# Open Session

Prints the Dash0 session URL and opens it in the browser.

## Steps

1. Get the session URL by running:

```bash
echo '{"session_id": "<current_session_id>"}' | CLAUDE_PLUGIN_OPTION_OTLP_URL="${CLAUDE_PLUGIN_OPTION_OTLP_URL}" DASH0_OTLP_URL="${DASH0_OTLP_URL}" "${CLAUDE_PLUGIN_ROOT}/scripts/on-event.sh" session-url
```

Replace `<current_session_id>` with the session ID from `$CLAUDE_SESSION_ID`.

2. Print the URL to the user.

3. Open it in the default browser:
   - macOS: `open <url>`
   - Linux: `xdg-open <url>`
