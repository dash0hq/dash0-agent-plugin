# copilot-app/turns

How a Copilot app session's prompts become `chat` spans: where a turn starts and ends, how a failed
one is reported, what a message sent into a busy session does, and whose tokens a turn carries.
Every spec runs from the runner procedure in [../README.md](../README.md). The failure and token
specs use the fake model, so their failures and counts are decided by the run, not the model.

| Invariant | Spec | Fake model | Status |
| --- | --- | --- | --- |
| A first turn that fails before the extension listens is still reported, with its error | [failed-first-turn-is-reported](failed-first-turn-is-reported.md) | error | draft — regression for the fix of 2026-10-06 |
| Under `omit_io`, a failed turn's status names only the error's category | [failed-turn-under-omit-io-names-only-the-category](failed-turn-under-omit-io-names-only-the-category.md) | error | draft |
| A later turn's error fails that turn's span and not the one before | [failed-later-turn-is-reported](failed-later-turn-is-reported.md) | ok, then error | draft |
| Each turn carries exactly its own tokens | [turn-tokens-are-exact](turn-tokens-are-exact.md) | ok | draft |
| Turns after the first are traced once each, from the live stream | [later-turns-are-traced-live](later-turns-are-traced-live.md) | — | draft — passed 13/13 on 2026-10-06 |
| A steering message joins the running turn instead of starting one | [steering-joins-the-running-turn](steering-joins-the-running-turn.md) | — | draft — passed 12/12 on 2026-10-06 |
| A queued message is its own turn, with its own tools | [queued-message-is-its-own-turn](queued-message-is-its-own-turn.md) | — | draft — passed 11/11 on 2026-10-06 |

## Deliberately not written

- **A turn the user stops, and a session reopened after an app restart.** No tool an agent has can
  press Stop or quit the app it runs in, so neither can run autonomously. The extension tests cover
  both: `TestCopilotAppExtension_failureReachesTheBinary` and
  `TestCopilotAppExtension_resumedSessionReplaysNothing`.
- **An error the app recovers from.** Whether the app retries a failed request by itself (a `429`,
  say) has not been measured, so there is no input known to produce a recovered turn.
- **Whether the session's state directory is removed at session end.** It is the plugin's private
  state, and its expected value could only come from the implementation. Unit tests cover it.
