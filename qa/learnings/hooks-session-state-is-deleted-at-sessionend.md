# Per-session plugin state is deleted at SessionEnd, so it cannot be inspected afterwards

`internal/pipeline/pipeline.go` removes the session directory under
`$CLAUDE_PLUGIN_DATA/<session_id>/` when it handles `SessionEnd`. Looking for that
directory after a session is therefore never evidence about whether the plugin ran: it is
absent for a healthy session and absent for one that never fired.

This wasted a diagnosis. The absence of a session directory was read as proof that the
installed plugin had not participated in a QA session. Polling the same path *during* a
run showed the directory appearing and then being cleaned up, which reversed the
conclusion.

**Why it matters:** the question "did the installed plugin also fire?" decides whether a
QA session double-exports, and the obvious way to answer it gives a confident wrong
answer.

**How to apply:** poll during the run, not after. Run the session in the background,
sample the data root every second, and search the samples for the session id afterwards.
Any question about per-session plugin state has to be asked while the session is alive.
