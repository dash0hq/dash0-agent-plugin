# Test image for the Dash0 agent plugin.
#
# Builds the on-event binary from local source, installs Claude Code and git,
# and installs THIS plugin globally from a local marketplace — so the locally
# built binary is used instead of the one published to GitHub Releases.
#
# Build (from the repo root, NOT from this directory):
#   docker build -f scripts/docker/Dockerfile -t dash0-claude-test .
#
# Run (the Dash0 endpoint is baked in; pass your token via DASH0_TOKEN):
#   docker run -it -e DASH0_TOKEN="<your-token>" dash0-claude-test
#   # then inside the container:
#   claude            # log in manually on first launch
#
# Optional extra config at run time:
#   -e DASH0_DATASET="default"
#   -e DASH0_TEAM_NAME="my-test-team"

# ---- Stage 1: build the on-event binary from local source ----
FROM golang:1.25-bookworm AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -o /out/on-event ./cmd/on-event/

# ---- Stage 2: runtime ----
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
      git \
      curl \
      ca-certificates \
      bash \
    && rm -rf /var/lib/apt/lists/*

# Install Claude Code (native installer -> ~/.local/bin). Symlink into
# /usr/local/bin so `claude` resolves in login shells too (which reset PATH).
RUN curl -fsSL https://claude.ai/install.sh | bash \
    && ln -sf /root/.local/bin/claude /usr/local/bin/claude
ENV PATH="/root/.local/bin:${PATH}"

# Lay out a local marketplace that references the plugin source.
#   /opt/dash0-marketplace/.claude-plugin/marketplace.json  -> the marketplace
#   /opt/dash0-marketplace/dash0-agent-plugin/              -> the plugin
COPY scripts/docker/marketplace.json /opt/dash0-marketplace/.claude-plugin/marketplace.json
COPY .claude-plugin /opt/dash0-marketplace/dash0-agent-plugin/.claude-plugin
COPY hooks          /opt/dash0-marketplace/dash0-agent-plugin/hooks
COPY scripts        /opt/dash0-marketplace/dash0-agent-plugin/scripts

# Drop the locally built binary into the plugin's data dir under the exact
# name on-event.sh expects, so it skips the GitHub Releases download.
# Data dir convention: ~/.claude/plugins/data/<plugin-name>-<marketplace-name>/
COPY --from=builder /out/on-event /tmp/on-event
RUN set -eux; \
    VERSION="$(sed -n 's/^VERSION="\(.*\)"/\1/p' /opt/dash0-marketplace/dash0-agent-plugin/scripts/on-event.sh)"; \
    ARCH="$(dpkg --print-architecture)"; \
    BIN_DIR="/root/.claude/plugins/data/dash0-agent-plugin-dash0-local/bin"; \
    mkdir -p "$BIN_DIR"; \
    install -m 0755 /tmp/on-event "$BIN_DIR/on-event-${VERSION}-linux-${ARCH}"; \
    rm -f /tmp/on-event

# Install the plugin globally (user scope). No login required for this step;
# it only reads the local marketplace and writes ~/.claude/settings.json.
RUN claude plugin marketplace add /opt/dash0-marketplace \
    && claude plugin install dash0-agent-plugin@dash0-local --scope user \
    && claude plugin list

# Dash0 endpoint is baked in; the token is supplied at run time via DASH0_TOKEN
# (mapped to the plugin's auth-token option by the entrypoint).
ENV DASH0_OTLP_URL="https://ingress.eu-west-1.aws.dash0-dev.com:4318"

COPY scripts/docker/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

# A scratch project directory to launch Claude Code from.
WORKDIR /workspace

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["bash"]
