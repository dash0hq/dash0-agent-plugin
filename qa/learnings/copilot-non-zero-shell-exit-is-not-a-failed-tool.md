# On Copilot a command that exits non-zero is a successful tool call

Asking Copilot to run `exit 3` produces a `postToolUse` payload with
`"resultType": "success"` and a native `execute_tool` span with no error status. The
result text says `<shellId: 0 completed with exit code 3>` and that is the only trace of
the failure. Measured 2026-08-28 on `qa/runs/probe-tool-failure` against Copilot CLI
1.0.80.

The plugin is faithful here: it takes the native span's status, and the native span says
the tool worked. So the Dash0 span carries no error either.

**Why it matters:** the obvious way to write a "failed tool sets the span status" spec on
this runtime is a shell command that fails, and it will report a defect that is not one.

**How to apply:** a Copilot spec about `exception.message` or a failed span status needs a
tool that fails at the **tool** level, not a command that fails at the shell level. None
has been found that fails on demand, which is why that invariant is listed as not written
in `qa/specs/copilot/session/README.md`. `qa/tools/qa-otel.py` reports `failed_tools` from
the native status, so the day such an input exists the channel is already there.
