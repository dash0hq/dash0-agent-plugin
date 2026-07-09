#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

# If DASH0_TOKEN is provided at run time, expose it to the plugin as the auth
# token. The plugin reads the token only from CLAUDE_PLUGIN_OPTION_AUTH_TOKEN
# (it has no DASH0_ fallback, by design), so map it here. Exported vars survive
# the exec below and are inherited by `claude` and its hook subprocesses.
if [ -n "${DASH0_TOKEN:-}" ]; then
  export CLAUDE_PLUGIN_OPTION_AUTH_TOKEN="$DASH0_TOKEN"
fi

exec "$@"
