# A project-level config file reconfigures every hook registration in the session

`claude/claude-on-event.sh` reads `.claude/dash0-agent-plugin.local.md` relative to the
hook's working directory and exports its values into the environment. That happens in
every process the session spawns for a hook, not only the one the file was written for.

`auth_token` is the dangerous key. The wrapper exports it as
`CLAUDE_PLUGIN_OPTION_AUTH_TOKEN`, which is the *highest* precedence form, so it
overrides the real token even for the installed plugin. The installed plugin then keeps
its own endpoint from [[config-managed-options-cannot-be-overridden]] and posts to the
real ingress with a QA token.

The symptom is silence in both directions: the export gets a 401 and is dropped, and
querying Dash0 for the session returns zero spans. Six probe sessions produced no spans
this way before the cause was found.

**Why it matters:** it is the most expensive failure in this setup, because every channel
agrees that nothing arrived and nothing points at the credential as the reason. A 401 on
a hook export is not surfaced to the user.

**How to apply:** never write a `dash0-agent-plugin.local.md` into a QA project
directory. A developer's own configuration belongs in `~/.claude/`, where a QA project
does not pick it up as project-level. `setup.md`'s
`no-project-config-overrides-the-install` check guards the driver against regrowing this.
