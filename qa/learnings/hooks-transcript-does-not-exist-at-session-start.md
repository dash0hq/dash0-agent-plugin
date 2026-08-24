# The first hooks of a session carry a transcript path that does not exist yet

`SessionStart`, `InstructionsLoaded`, and `UserPromptSubmit` all arrive with a
`transcript_path`, and opening it fails with `no such file or directory`. Claude Code
names the transcript before it writes it. From `PreToolUse` onwards the file exists and
grows between every hook: one small session went 67 KB, 70 KB, 72 KB, 76 KB across four
consecutive hooks.

`internal/transcript` reads the same path and sees the same absence, so this is the
system behaving normally rather than a race in the observer.

**Why it matters:** a recorder that treats the missing file as an error reports three
failures on every healthy session, and real failures then get lost in the noise. In the
other direction, a fixture built from a single end-of-session transcript copy does not
represent what the pipeline read at each hook.

**How to apply:** classify a missing transcript as absent, not failed, and keep the
record. Snapshot the transcript per invocation rather than once at the end, and
content-address the snapshots so an unchanged file costs one copy. For a token count that
must be exact, use the final transcript, which every reader saw complete.
