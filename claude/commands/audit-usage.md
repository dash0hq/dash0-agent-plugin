---
description: Audit a session's token usage from the local transcripts, to compare against Dash0.
argument-hint: "[session-id]"
---

# Audit Usage

Reconstructs per-model token counts for a Claude Code session — the main session
plus every sub-agent it spawned — from the transcripts on disk, and reports how
many spans Dash0 should hold for it. Use it when the token or cost numbers in
Dash0 look wrong: it produces the ground truth to compare them against.

## Steps

1. Pick the session: `$1` if the user passed one, otherwise `$CLAUDE_SESSION_ID`.

2. Run:

```bash
python3 "${CLAUDE_PLUGIN_ROOT}/scripts/claude-code-usage-audit.py" <session-id>
```

   If `python3` is not installed, say so and stop — the script needs it. If the
   script reports no transcript for the session, run it with no argument to list
   the sessions on disk and ask the user which one they meant.

3. Leave the script's output as it was printed — do not reformat or summarize the
   numbers. Repeat it verbatim if it is not already visible to the user, as in a
   non-interactive run. It is meant to be read side by side with the other two
   numbers, or pasted into a bug report.

4. Then say what to compare it against:
   - Claude Code's own numbers: run `/usage` in that session.
   - Dash0: the spans whose `gen_ai.conversation.id` is the session id
     (`/open-session` opens that page).

   Usage or spans that appear in the audit but not in Dash0 never arrived. The
   sub-agent rows correspond to the `invoke_agent` spans.

5. If the session audited is the current one, add that the turn in progress has
   not finished, so its usage and its spans are still incomplete — for a clean
   comparison, audit a session that has ended.
