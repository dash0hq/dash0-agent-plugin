---
description: Audit an OpenCode session's token usage from local storage, to compare against Dash0.
---

# Audit Usage

Reconstruct a session's token counts — the main session plus every sub-agent it
spawned — from OpenCode's own storage, and report what Dash0 should hold for it.
Use it when the token or cost numbers in Dash0 look wrong: this produces the
ground truth to compare them against.

Session id: `$ARGUMENTS` if the user passed one, otherwise the top row of
`opencode session list`.

## Steps

1. Read the session's own totals, and any sub-agent sessions beneath it, in one
   query. `parent_id` is what makes a row a sub-agent:

```bash
opencode db --format json "
  select id, parent_id, tokens_input, tokens_output, tokens_reasoning,
         tokens_cache_read, tokens_cache_write, cost
  from session
  where id = '<session-id>' or parent_id = '<session-id>'
  order by parent_id nulls first"
```

2. Break the parent session down per model, which is where a mismatch usually
   shows up:

```bash
opencode export <session-id> | jq '
  [.messages[].info | select(.role == "assistant")]
  | group_by(.providerID + "/" + .modelID)
  | map({model: .[0].providerID + "/" + .[0].modelID,
         messages: length,
         input: map(.tokens.input) | add,
         output: map(.tokens.output) | add,
         reasoning: map(.tokens.reasoning) | add,
         cache_read: map(.tokens.cache.read) | add,
         cache_write: map(.tokens.cache.write) | add})'
```

   `opencode export` covers one session, so run it once per sub-agent id from
   step 1 as well.

3. Report the numbers as the tools printed them. Do not reformat or re-add them
   — the point is to have something to paste into a bug report next to the other
   two figures.

4. Then say what to compare against:

   - OpenCode's own accounting: `opencode stats --models`.
   - Dash0: the spans whose `gen_ai.conversation.id` is the session id.
     `/open-session` opens that page.

   The parent session's totals belong on its `chat` span; each sub-agent row
   belongs on an `invoke_agent` span. Usage that appears here but not in Dash0
   never arrived.

5. Two things that are not discrepancies, and are worth saying before the user
   files a bug:

   - If the audited session is the current one, the turn in progress has not
     finished, so its usage and its spans are both incomplete. Audit a session
     that has ended for a clean comparison.
   - `tokens_cache_write` is zero for providers whose API reports no
     cache-write figure. A zero here and a zero in Dash0 agree.
