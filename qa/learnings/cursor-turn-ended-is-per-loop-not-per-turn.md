# Cursor's turn_ended marker fires once per agent loop, not once per turn

A Cursor transcript ends with `{"type": "turn_ended", "status": "success"}`, and the name
invites reading it as a per-turn marker. It is not.

Measured 2026-09-01 on `qa/runs/setup-probe-cursor-turns`, a two-turn session: six message
entries — two `<user_query>` prompts, four assistant entries — and **one** `turn_ended`, at the
end of the last turn. The single-turn probe had one prompt and one `turn_ended`, which is why
the wrong reading looked right first.

Counting the marker reported that two-turn run as `chat: Dash0 has 2, the transcript implies 1`
— a healthy session with one `chat` span too many.

**Why it matters:** it is the only obviously turn-shaped field in the file, and it is a
per-session marker wearing a per-turn name.

**How to apply:** count the submitted prompts instead. Cursor wraps every one in
`<user_query>`, which no tool result and no system entry carries, so a user-role text block
containing that tag is exact. `qa-transcript-cursor.py` reports both — `turns` from the prompts
and `loop_ends` from the marker — so the distinction stays visible rather than being buried in
one number.
