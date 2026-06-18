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
DASH0_DATASET=demo \
./internal/demo/deploy/deploy.sh
```

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

## Configuration

| Variable           | Required | Default              | Purpose                              |
| ------------------ | -------- | -------------------- | ------------------------------------ |
| `DASH0_OTLP_URL`   | yes      | —                    | OTLP ingress URL                     |
| `DASH0_AUTH_TOKEN` | yes      | —                    | Dash0 auth token (stored as env var) |
| `DASH0_DATASET`    | no       | `demo`               | Target dataset                       |
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
