#!/usr/bin/env bash
set -euo pipefail

# Demo data generator for Claude Code Agent Monitoring
# Run from a repo where the plugin is project-installed (e.g. ~/source/dash0).
#
# Usage: ./scripts/demo-generate.sh <otlp-url> <auth-token> <dataset> [work-dir]
#
# Example:
#   ./scripts/demo-generate.sh \
#     https://ingress.eu-west-1.aws.dash0.com:4318 \
#     auth_YourTokenHere \
#     claude-code-monitoring \
#     ~/source/dash0

if [ $# -lt 3 ]; then
  echo "Usage: $0 <otlp-url> <auth-token> <dataset> [work-dir]"
  echo ""
  echo "Arguments:"
  echo "  otlp-url    Dash0 OTLP endpoint (e.g. https://ingress.eu-west-1.aws.dash0.com:4318)"
  echo "  auth-token  Dash0 ingest auth token"
  echo "  dataset     Dash0 dataset name (e.g. claude-code-monitoring)"
  echo "  work-dir    Directory to run sessions from (default: ~/source/dash0)"
  echo ""
  echo "Example:"
  echo "  $0 https://ingress.eu-west-1.aws.dash0.com:4318 auth_xxx my-dataset ~/source/dash0"
  exit 1
fi

OTLP_URL="$1"
AUTH_TOKEN="$2"
DATASET="$3"
WORK_DIR="${4:-$HOME/source/dash0}"
ORIGINAL_USER=$(cd "$WORK_DIR" && git config user.name)
ORIGINAL_EMAIL=$(cd "$WORK_DIR" && git config user.email)

# Team members
declare -a USERS=("Alice Chen" "Bob Martinez" "Carol Park" "Dave Kowalski" "Eve Nakamura")
declare -a EMAILS=("alice@acme.dev" "bob@acme.dev" "carol@acme.dev" "dave@acme.dev" "eve@acme.dev")

LOCAL_MD="$WORK_DIR/.claude/dash0-agent-plugin.local.md"

cleanup() {
  echo ""
  echo "🔄 Restoring git config and removing local override..."
  cd "$WORK_DIR"
  git config user.name "$ORIGINAL_USER"
  git config user.email "$ORIGINAL_EMAIL"
  rm -f "$LOCAL_MD"
  echo "   Restored: $ORIGINAL_USER <$ORIGINAL_EMAIL>"
  echo "   Removed: $LOCAL_MD"
}
trap cleanup EXIT

# Create per-project override to send to the demo dataset.
mkdir -p "$WORK_DIR/.claude"
cat > "$LOCAL_MD" << EOF
---
enabled: true
otlp_url: "$OTLP_URL"
auth_token: "$AUTH_TOKEN"
dataset: "$DATASET"
---
EOF

run_session() {
  local user_index=$1
  local prompt=$2
  local model=${3:-"claude-sonnet-4-6"}

  local user="${USERS[$user_index]}"
  local email="${EMAILS[$user_index]}"

  echo ""
  echo "🤖 Session: $user ($model)"
  echo "   Prompt: ${prompt:0:60}..."

  cd "$WORK_DIR"
  git config user.name "$user"
  git config user.email "$email"

  echo "$prompt" | claude --print \
    --model "$model" \
    --max-turns 12 \
    --plugin-dir ~/.claude/plugins/cache/dash0/dash0-agent-plugin/0.1.5 \
    2>/dev/null || true

  echo "   ✅ Done"
  sleep 3
}

echo "🎬 Dash0 Agent Monitoring — Demo Data Generator"
echo "================================================"
echo ""
echo "Work dir: $WORK_DIR"
echo "Original user: $ORIGINAL_USER"
echo ""
echo "⚠️  Plugin must be installed and configured for this directory."
echo "   Generating 8 sessions from 5 team members..."
echo ""

# Session 1: Alice hunts a mysterious bug across multiple files
run_session 0 \
  "Help me debug something weird. 1) Run 'git log --oneline -10' to see recent changes. 2) Then read components/api/internal/retrieval/agentsessions/sessions.go — I think someone broke the query. 3) Now check if the test file exists and read its first 30 lines. 4) Run 'grep -rn groupUniqArray components/api/' to find all array aggregations. 5) Finally, tell me if you see any performance concerns with the current approach." \
  "claude-sonnet-4-6"

# Session 2: Bob does a massive refactoring investigation
run_session 1 \
  "I need to understand our UI component structure before refactoring. Do all of this: 1) Run 'find components/ui/src/agents -name \"*.tsx\" | wc -l' to count agent components. 2) Read the main sessions table file at components/ui/src/agents/components/agent-monitoring/sessions/use-agent-sessions-table-definition.tsx. 3) Run 'grep -rn \"export function\" components/ui/src/agents/components/agent-monitoring/sessions/' to find all exports. 4) Read the session-stats.ts file. 5) Check how many test files exist: 'find components/ui/src/agents -name \"*.test.*\" | head -10'. 6) Tell me which components have the most responsibility and should be split." \
  "claude-opus-4-7"

# Session 3: Carol does an absolutely thorough security sweep
run_session 2 \
  "Time for our quarterly security review. Please: 1) Run 'git ls-files | grep -i \\.env' to find env files. 2) Run 'grep -rn \"password\\|secret\\|token\" components/api/api.yaml' to check for hardcoded creds. 3) Check 'cat .gitignore | grep -i env\\|secret\\|key' to verify ignore patterns. 4) Run 'find . -name \"*.pem\" -o -name \"*.key\" | grep -v node_modules' for leaked certs. 5) Read the first 20 lines of components/api/api.yaml to check what's exposed. 6) Run 'grep -rn \"insecureSkipVerify\" components/' to find TLS bypasses. 7) Give me a security report card." \
  "claude-opus-4-7"

# Session 4: Dave writes a comprehensive test plan
run_session 3 \
  "I'm writing tests for the agent sessions feature. Help me: 1) Read components/api/internal/retrieval/agentsessions/sessions_test.go fully. 2) Run 'grep -c \"func Test\" components/api/internal/retrieval/agentsessions/sessions_test.go' to count test functions. 3) Read sessions.go and identify all public functions. 4) Compare — which functions have no test? 5) Run 'grep -rn \"assert\\|require\" components/api/internal/retrieval/agentsessions/sessions_test.go | wc -l' to gauge assertion density. 6) Suggest 3 additional test cases we're missing with mock data examples." \
  "claude-sonnet-4-6"

# Session 5: Eve goes deep on performance optimization
run_session 4 \
  "Our ClickHouse queries might be slow. Let's investigate: 1) Read the main query in components/api/internal/retrieval/agentsessions/sessions.go. 2) Count the number of coalesce/nullIf/anyIf calls (run: grep -c 'coalesce\\|nullIf\\|anyIf' components/api/internal/retrieval/agentsessions/sessions.go). 3) Check if there are materialized columns: run 'find components -path \"*db-migrator*\" -name \"*.md\" | head -5' then read the traces schema doc. 4) Run 'wc -l components/api/internal/retrieval/agentsessions/sessions.go' to see how big the file is. 5) Check 'grep -c \"SpanAttributes\" components/api/internal/retrieval/agentsessions/sessions.go' — each map lookup is expensive. 6) Propose an optimization plan with estimated impact." \
  "claude-opus-4-7"

# Session 6: Alice asks a quick question (short session for contrast — the 'just checking' developer)
run_session 0 \
  "What Go version are we using? Check components/api/go.mod" \
  "claude-sonnet-4-6"

# Session 7: Bob explores the entire CI/CD pipeline
run_session 1 \
  "I need to understand our CI. Please: 1) Run 'find .github/workflows -name \"*.yml\" | sort'. 2) Read the first 50 lines of .github/workflows/ci-cd-v2.yml (or whatever the main one is called). 3) Run 'grep -l \"test\\|Test\" .github/workflows/*.yml' to find test-related workflows. 4) Check 'grep -c \"runs-on\" .github/workflows/ci-cd-v2.yml' to count jobs. 5) Look for any collector-specific CI: 'grep -rl \"collector\" .github/workflows/'. 6) How long do you think our CI takes based on the number of jobs? What would you optimize first?" \
  "claude-opus-4-7"

# Session 8: Carol reviews the whole dependency tree (she's paranoid, we love her)
run_session 2 \
  "Dependency audit time! 1) Run 'cat components/ui/package.json | python3 -c \"import json,sys; d=json.load(sys.stdin); print(len(d.get(\"dependencies\",{})), \"deps\", len(d.get(\"devDependencies\",{})), \"devDeps\")\"'. 2) Check for React version: 'grep react components/ui/package.json | head -3'. 3) Run 'grep -c \"@dash0\" components/ui/package.json' for internal deps. 4) Check Go deps: 'wc -l components/api/go.mod'. 5) Run 'grep \"replace\" components/api/go.mod | wc -l' for local replacements. 6) Look for any deprecated warnings: 'grep -i deprecated components/ui/package.json | head -5'. 7) Rate our dependency health on a scale of 1-10." \
  "claude-sonnet-4-6"

# Session 9: Dave reverse-engineers the collector architecture
run_session 3 \
  "I'm new to the collector codebase. Walk me through it: 1) Run 'ls components/collector/processor/ | wc -l' to count processors. 2) Run 'ls components/collector/processor/' to see names. 3) Read components/collector/processor/dash0claudecodenormalizerprocessor/processor.go — that's our newest one. 4) Read its test file too. 5) Check 'cat components/collector/builder-config.yaml | grep -A1 \"dash0claude\"'. 6) Run 'find components/collector -name \"metadata.yaml\" | wc -l' to count all components. 7) How does our collector compare in size to a typical OTel collector deployment?" \
  "claude-opus-4-7"

# Session 10: Eve does a full observability readiness assessment
run_session 4 \
  "Let's assess our own observability maturity (yes, observability company eating our own dogfood): 1) Run 'find . -name \"*otel*\" -o -name \"*telemetry*\" | grep -v node_modules | grep -v .git | wc -l'. 2) Check 'cat components/collector/collector-config.yaml'. 3) Run 'grep -rn \"metric\\|Metric\" modules/prom-querier/internal/querier/ --include=\"*.go\" -l | wc -l' to count metric files. 4) Read docs/agent-monitoring-attributes.md — this is our own documentation. 5) Run 'find . -name \"*dashboard*\" -path \"*hub*\" | head -5' to check dashboards. 6) Run 'grep -rn \"gen_ai\" modules/prom-querier/ --include=\"*.go\" | wc -l' for gen_ai metric coverage. 7) Give us an observability maturity score and explain what's missing." \
  "claude-sonnet-4-6"

# Session 11: Alice tries to use the Dash0 MCP server (will trigger mcp__ tool calls if available)
run_session 0 \
  "I want to check our own production metrics. Can you query Dash0 using the MCP server? Try: 1) List available services in Dash0. 2) Check if there are any gen_ai metrics available. 3) What's our total span count in the last hour? If MCP isn't available, just tell me what tools you have access to and how I could set up the Dash0 MCP server." \
  "claude-opus-4-7"

# Session 12: Bob has a late-night existential crisis about code quality
run_session 1 \
  "It's 2am and I can't sleep because I'm worried about code quality. Help me assess the damage: 1) Run 'find components/api -name \"*.go\" | xargs wc -l | sort -n | tail -10' to find the biggest Go files. 2) Run 'find components/ui/src -name \"*.tsx\" | xargs wc -l 2>/dev/null | sort -n | tail -10' for biggest UI files. 3) Check 'git log --oneline --since=\"1 week ago\" | wc -l' — how active is development? 4) Run 'grep -rn \"TODO\\|FIXME\\|HACK\" components/api/ --include=\"*.go\" | wc -l' for tech debt markers. 5) Run 'grep -rn \"TODO\\|FIXME\" components/ui/src/ --include=\"*.tsx\" --include=\"*.ts\" | wc -l' for UI tech debt. 6) Based on file sizes and tech debt density, which area needs love first? Be brutally honest." \
  "claude-sonnet-4-6"

echo ""
echo "================================================"
echo "🎉 Demo data generation complete!"
echo ""
echo "Sessions generated: 12"
echo "Users: ${USERS[*]}"
echo "Models: claude-sonnet-4-6, claude-opus-4-7"
echo ""
echo "View sessions at:"
echo "  https://app.dash0.com/agent-monitoring/claude-code"
