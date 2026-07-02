# Deploying the demo generator to AWS Lambda

Runs the demo telemetry generator as a Lambda function triggered by an
EventBridge rule (every 20 minutes by default). Deploy from your own machine
with the AWS CLI — no CI or extra framework required.

## Prerequisites

- AWS CLI v2, authenticated (env vars, `--profile`, or SSO) with permission to
  manage Lambda, IAM, and EventBridge.
- Go 1.25+ and `zip` on your PATH (the script cross-compiles the binary).

## Deploy

```sh
DASH0_OTLP_URL=https://ingress.<region>.aws.dash0.com \
DASH0_AUTH_TOKEN=<auth> \
./internal/demo/deploy/deploy.sh
```

Leave `DASH0_DATASET` unset to let the auth token pick the dataset, or set it
(e.g. `DASH0_DATASET=demo`) to target a specific one.

The script is idempotent — re-run it to ship new code or change config. It:

1. cross-compiles `./cmd/demo` to a `bootstrap` binary and zips it (the same
   binary serves the Lambda Runtime API loop when it detects the runtime);
2. creates (once) an IAM execution role with basic Lambda logging;
3. creates or updates the `dash0-demo-telemetry` function (`provided.al2023`, arm64);
4. creates/updates the EventBridge rule `rate(20 minutes)` and points it at the function.

### Verify

```sh
aws lambda invoke --region eu-west-1 --function-name dash0-demo-telemetry /dev/stdout
```

## Current deployments

Four functions are currently live. Each was deployed with the command below
(auth tokens redacted — get the real ones from the corresponding Dash0
environment). A distinct `FUNCTION_NAME` gives each its own independent Lambda,
IAM role, and EventBridge rule. The AWS account is `DevRel2`

| Function                          | Region      | Dataset            |
| --------------------------------- | ----------- | ------------------ |
| `dash0-coding-agents-demo-dev`    | `eu-west-1` | `demo-app` (dev)   |
| `dash0-coding-agents-demo-eu`     | `eu-west-1` | `Default`          |
| `dash0-coding-agents-demo-eu-wad` | `eu-west-1` | from token         |
| `dash0-coding-agents-demo-us`     | `us-west-2` | `Default`          |

```sh
# dev environment (dash0-dev.com)
DASH0_OTLP_URL=https://ingress.eu-west-1.aws.dash0-dev.com \
  DASH0_AUTH_TOKEN=auth_REDACTED \
  DASH0_DATASET=demo-app \
  FUNCTION_NAME=dash0-coding-agents-demo-dev \
  ./internal/demo/deploy/deploy.sh

# EU production
DASH0_OTLP_URL=https://ingress.eu-west-1.aws.dash0.com \
  DASH0_AUTH_TOKEN=auth_REDACTED \
  DASH0_DATASET=Default \
  FUNCTION_NAME=dash0-coding-agents-demo-eu \
  ./internal/demo/deploy/deploy.sh

# EU production, WeAreDevelopers (dataset derived from token)
DASH0_OTLP_URL=https://ingress.eu-west-1.aws.dash0.com \
  DASH0_AUTH_TOKEN=auth_REDACTED \
  FUNCTION_NAME=dash0-coding-agents-demo-eu-wad \
  ./internal/demo/deploy/deploy.sh

# US production
DASH0_OTLP_URL=https://ingress.us-west-2.aws.dash0.com \
  DASH0_AUTH_TOKEN=auth_REDACTED \
  DASH0_DATASET=Default \
  FUNCTION_NAME=dash0-coding-agents-demo-us \
  ./internal/demo/deploy/deploy.sh
```

## Configuration

| Variable           | Required | Default              | Purpose                              |
| ------------------ | -------- | -------------------- | ------------------------------------ |
| `DASH0_OTLP_URL`   | yes      | —                    | OTLP ingress URL                     |
| `DASH0_AUTH_TOKEN` | yes      | —                    | Dash0 auth token (stored as env var) |
| `DASH0_DATASET`    | no       | derived from token   | Target dataset; unset = no `Dash0-Dataset` header, ingest uses the token's dataset |
| `DEMO_TURNS`       | no       | `1`                  | Turns sent per invocation            |
| `AWS_REGION`       | no       | `eu-west-1`          | Target region                        |
| `FUNCTION_NAME`    | no       | `dash0-demo-telemetry` | Lambda + base for role/rule names  |
| `SCHEDULE`         | no       | `rate(20 minutes)`   | EventBridge schedule expression      |
| `ARCH`             | no       | `arm64`              | `arm64` or `x86_64`                  |

The auth token is stored as a plain Lambda environment variable, which is fine
for a demo account. Don't point this at a production token.

## Tear down

```sh
./internal/demo/deploy/teardown.sh
```

Removes the rule, target, function, and role (matching the same `FUNCTION_NAME`
/ `AWS_REGION`).

## Note on data density

Each invocation sends `DEMO_TURNS` turn(s), and every turn randomizes the repo
and branch. Rate-based queries like `increase(...[10m])` need a series to span
the window, so very sparse data can look empty. Raise `DEMO_TURNS` (or shorten
`SCHEDULE`) to populate the views faster.
