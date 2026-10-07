# opencode-v2/turns

Turn boundaries: two turns in one session, and one turn that survives a reload.

| Spec | Asserts |
| --- | --- |
| [resumed-turn-is-scoped-to-itself](resumed-turn-is-scoped-to-itself.md) | Each turn is its own trace, with its own token count and its own messages |
| [reload-mid-turn-keeps-the-turn](reload-mid-turn-keeps-the-turn.md) | An `opencode reload` during a tool call replaces the exporter and the turn still reconciles |
