# Demo telemetry generator

Generates mock Claude Code telemetry and sends it to Dash0, simulating users
exercising the agent plugin so the agent-monitoring views have realistic data.

## What one invocation produces

Exactly **one agent turn**:

- a root `chat <model>` span (the LLM turn), and
- a single child `execute_tool <tool>` span — a Bash command, an MCP tool call,
  or a Skill invocation, chosen at random.

Both spans share a trace; the tool span's parent is the chat span.

Randomized per turn from closed lists in [`data.go`](./data.go):

- **repository** — one of 6
- **user** — one of 40 (each hard-coded to one of 6 **teams**, emitted as `dash0.team.name`)
- **branch** — `ENG-<random>-<title>` from a list of titles
- **model**, **effort**, and the **prompt/response pair** (one of 20)
- **token usage** — random input/output/cache counts

A fresh session (`gen_ai.conversation.id`) is created for every turn. **Cost is
not emitted** — it is enriched in the Dash0 backend.

Alongside the spans, each turn also exports the `dash0.gen_ai.vcs.*` metrics
(change count, time to merge, time to approval, lines added/deleted) with
randomized values. They carry the **same repository and branch** as the turn's
spans, so the metrics join with the spans on the VCS dimensions. See
[`vcs_metrics.go`](./vcs_metrics.go).

## Running locally

```sh
# Send one turn to Dash0
go run ./cmd/demo -url https://ingress.<region>.aws.dash0.com -token <auth> -dataset demo

# Or configure via env, and send a batch of 25 turns
DASH0_OTLP_URL=... DASH0_AUTH_TOKEN=... DASH0_DATASET=demo go run ./cmd/demo -n 25

# Print payloads without sending anything
go run ./cmd/demo -debug
```

## Layout

- [`data.go`](./data.go) — the closed lists of mock data.
- [`generator.go`](./generator.go) — `GenerateTurn` builds the OTLP request for one turn.
- [`handler.go`](./handler.go) — `Handle` generates one turn and exports it; the
  transport-agnostic entry point shared by the local driver and (later) a Lambda wrapper.
- [`../../cmd/demo`](../../cmd/demo) — the binary: a local CLI that also serves
  the Lambda Runtime API loop when run on Lambda (`lambda.go`).
- [`deploy/`](./deploy) — deploy to Lambda on an EventBridge schedule (see its README).
- [`spans/`](./spans) — captured span templates the generator's schema mirrors.

## Deploying

See [`deploy/README.md`](./deploy/README.md) to run this on a schedule as an AWS
Lambda function (EventBridge, every 20 minutes), driven from your machine with
the AWS CLI.
