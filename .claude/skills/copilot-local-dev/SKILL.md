---
name: copilot-local-dev
description: Install and run the Dash0 GitHub Copilot CLI plugin (the copilot/ package) locally for development/testing — no GitHub push or release needed. Use when you want to try or test the Copilot plugin against a real `copilot` session, iterate on it, or tear the setup down.
---

# Run the Dash0 Copilot plugin locally

The public install path (`copilot plugin install dash0hq/dash0-agent-plugin:copilot`)
needs the branch pushed and a release carrying the `copilot-on-event` binary. For
**local dev** we skip both by registering a throwaway **local marketplace** that
points at this repo's `copilot/` package, installing it the real way, and dropping
a **locally-built** binary where the bootstrap looks (so it skips the download).

This exercises the actual packaging — manifest loading, camelCase `hooks.json`,
`${PLUGIN_ROOT}` resolution, the `dash0-configure` skill, the bare-install guard —
not a hand-wired approximation.

## Install / update

Run the setup script (idempotent):

```bash
.claude/skills/copilot-local-dev/setup.sh
```

It stages the marketplace, `marketplace add` + `plugin install`s it, and builds
the binary into `~/.copilot/plugin-data/dash0-local/dash0-agent-plugin/bin/`.

Then, **once**, set credentials + enable native OTel — start `copilot` and run:

```
/dash0-configure
```

(That writes `~/.copilot/dash0-agent-plugin.local.md` and installs a launch
function enabling Copilot's native OTel into
`~/.local/state/dash0-agent-plugin/copilot/otel` — the source of per-turn
token/cost/model.) Open a **fresh shell**, then use `copilot` normally; tool and
chat spans land in your Dash0 dataset.

## Iterate

- **Changed Go code** (`cmd/copilot-on-event`, `internal/source/copilot`): rebuild only —
  `.claude/skills/copilot-local-dev/setup.sh --rebuild` — then start a new `copilot` session.
- **Changed plugin files** (`copilot/hooks.json`, `plugin.json`, the skill, the bootstrap):
  full re-run — `.claude/skills/copilot-local-dev/setup.sh` — to reinstall.

## Teardown

```bash
.claude/skills/copilot-local-dev/teardown.sh
```
Removes the plugin, the marketplace, the plugin-data + OTel dirs, and the config
file. (Also remove the launch function / `COPILOT_OTEL_*` exports from your shell
profile if you added them.)

## How it maps to the real install (and caveats)

- `${PLUGIN_ROOT}` → `~/.copilot/installed-plugins/dash0-local/dash0-agent-plugin`;
  `${COPILOT_PLUGIN_DATA}` → `~/.copilot/plugin-data/dash0-local/dash0-agent-plugin`.
  The `plugin-data/…/bin` dir is created lazily on first hook run; setup.sh pre-creates it and drops the binary.
- Native OTel is **not** something the plugin can enable itself (no plugin `env`/OTel hook);
  it needs the launch-time env, which is exactly what `/dash0-configure`'s launch function provides.
- setup.sh copies `copilot/` **excluding `capture/` and `captured/`** (dev harness + git-ignored
  raw captures). The real GitHub subpath install would still include the tracked `capture/` dir —
  a packaging-cleanup item worth addressing before release (exclude `capture/` from the shipped plugin).
