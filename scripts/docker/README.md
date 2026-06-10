# Docker test image

A self-contained image for testing the Dash0 agent plugin from **local source**.
It installs Claude Code + git, builds the `on-event` binary from this repo, and
installs the plugin globally from a local marketplace — so the locally built
binary is used instead of the one published to GitHub Releases.

## Build

Run from the **repo root** (the build context must be the repo root, not this
directory):

```bash
docker build -f scripts/docker/Dockerfile -t dash0-claude-test .
```

## Run

The Dash0 endpoint (`https://ingress.eu-west-1.aws.dash0-dev.com:4318`) is baked
into the image. Supply your auth token via `DASH0_TOKEN`:

```bash
docker run -it -e DASH0_TOKEN="<your-token>" dash0-claude-test
# then inside the container:
claude            # log in manually on first launch
```

The plugin is already installed and enabled globally — no `--plugin-dir` flag
needed. On session start you should see `dash0: connected`.

## Optional run-time config

```bash
docker run -it \
  -e DASH0_TOKEN="<your-token>" \
  -e DASH0_DATASET="default" \
  -e DASH0_TEAM_NAME="my-test-team" \
  dash0-claude-test
```

Notes:
- Set `DASH0_TEAM_NAME` to verify the `dash0.team.name` span attribute.
- `DASH0_TOKEN` is mapped to the plugin's auth-token option
  (`CLAUDE_PLUGIN_OPTION_AUTH_TOKEN`) by the entrypoint, since the token has no
  `DASH0_` fallback by design.
- Without `DASH0_TOKEN`, the endpoint is still set but ingestion will fail auth —
  you'll see a connectivity warning on session start.

## How it works

- The plugin's `scripts/on-event.sh` only downloads the release binary if one
  isn't already present at
  `~/.claude/plugins/data/<plugin>-<marketplace>/bin/on-event-<version>-<os>-<arch>`.
  The Dockerfile pre-places the locally built binary there, so the download is
  skipped and your local code runs.
- The local marketplace is defined in `marketplace.json` and added at build time
  via `claude plugin marketplace add`, then installed with
  `claude plugin install ... --scope user`. Neither step requires authentication.
