# The dash0 CLI's active profile is the wrong source for a QA query

`dash0 config show` resolves a profile from disk plus any `DASH0_*` variables in the
shell. On a developer machine those variables are often set for something else entirely,
so the effective dataset is whatever they were last used for. Here it resolved to a
dataset the QA token cannot read, and the query failed with
`403 access to dataset '<name>' is not permitted`.

`--api-url`, `--auth-token`, and `--dataset` all override the profile, and passing all
three makes a query reproducible on any machine regardless of the shell it runs in.

**Why it matters:** the failure mode is worse when the profile happens to point at a
dataset the token *can* read. Then the query succeeds and returns nothing, which is
indistinguishable from the plugin having sent nothing.

**How to apply:** read `qa/config.local.json` and pass all three flags on every query.
`qa/tools/qa-compare.py` does this. For an ad-hoc query by hand, do the same rather than
relying on the ambient profile. Watch for the token in `argv`: strip it before pasting a
failing command anywhere.
