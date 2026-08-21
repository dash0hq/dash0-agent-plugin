# A throwaway HOME cannot run a real Claude Code session

Isolating a QA session with `HOME=/tmp/somewhere claude -p ...` looks like the clean way
to escape a developer's installed plugins and settings. It does not work: the session
starts and immediately returns `Not logged in · Please run /login`, with
`terminal_reason: api_error` and zero usage.

Credentials do not travel with the flags. Copying or symlinking them into the throwaway
HOME is not a fix worth making — it duplicates a live credential into a temporary
directory for the sake of an isolation that has other costs anyway.

**Why it matters:** it is the first idea anyone has for isolating a session, and it costs
a round of debugging that looks like an auth problem with the plugin rather than with the
harness.

**How to apply:** run in the real HOME and isolate at the project level instead: a
scratch working directory with its own `.claude/settings.json`. Accept that the installed
plugin participates, and design around it — see
[[config-managed-options-cannot-be-overridden]].
